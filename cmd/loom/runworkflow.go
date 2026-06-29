package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// newRunCmd is the parent for executing a workflow. Invoked with a workflow
// path it validates and runs (or resumes the latest run with --resume-latest);
// its `check` subcommand stops after validation and the printed plan. A path
// that is not a subcommand routes to the parent (cobra runs it when args[0]
// does not name a child), so `loom run wf.yaml` executes as before.
func newRunCmd() *cobra.Command {
	var (
		paramArgs    []string
		resumeLatest bool
	)
	cmd := &cobra.Command{
		Use:               "run <workflow>",
		Short:             "Validate and run a workflow",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			if resumeLatest {
				return doRunResumeLatest(cmd.OutOrStdout(), args[0], paramArgs)
			}
			return doRun(cmd.OutOrStdout(), args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	cmd.Flags().BoolVar(&resumeLatest, "resume-latest", false,
		"seed ok tasks from $LOOM_HOME/runs/<wf>/latest.json (default $HOME/.loom) and re-run the remainder")
	cmd.AddCommand(newCheckCmd())
	return cmd
}

// provenance records what initiated a run so the store can distinguish a
// scheduled run from a direct CLI invocation. A zero value marks a direct CLI
// run; only the daemon supplies one.
type provenance struct {
	scheduleID  string
	triggeredBy string
}

// runRequest bundles everything the unified run pipeline consumes: the parsed
// workflow and its inlined manifest, the resolved params, the resolved
// LOOM_HOME, the optional seed plan, and the provenance. wf, manifest, and
// resolved arrive together from the check phase, so grouping them keeps the
// pipeline signature stable. A zero plan and zero prov mark a plain direct run.
type runRequest struct {
	wf       *workflow.Workflow
	manifest []byte
	resolved workflow.ParamValues
	home     string
	plan     seedPlan
	prov     provenance
}

// doRun runs the shared check phase (validate + print the plan) and then, only
// if it passes, executes the whole workflow fresh. home is resolved up front (as
// the resume paths do) so a home-resolution failure surfaces before the plan.
func doRun(w io.Writer, path string, paramArgs []string) error {
	wf, manifest, _, err := loadWorkflow(path)
	if err != nil {
		return err
	}
	home, err := loomHome()
	if err != nil {
		return err
	}
	return renderCheckRun(w, runRequest{wf: wf, manifest: manifest, home: home}, paramInputs{cli: paramArgs}, nil)
}

// renderCheckRun runs the shared check phase (validate + print the plan) against
// a single renderer and, only if it passes, executes req. doRun and runFromRecord
// share this tail: one renderer drives both the check and the run that follows, so
// a stateful display spans both. params carries the CLI and (lower-precedence)
// file tiers; seeded annotates the plan with carried-over tasks. The caller
// fills req.wf, req.manifest, req.home, and any seed plan; the resolved params
// come from the check done here.
func renderCheckRun(w io.Writer, req runRequest, params paramInputs, seeded map[workflow.TaskID]bool) (err error) {
	r, finish := newRenderer(w)
	defer finish(&err)
	resolved, err := validateAndPlan(r, req.wf, params, false, seeded)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	req.resolved = resolved
	return runWorkflow(r, w, req)
}

// runWorkflow is the unified run pipeline shared by doRun, runFromRecord, and
// the daemon. It parses nothing itself; callers hand it the already-parsed
// workflow and resolved params after the check phase has already validated and
// printed the plan. req.plan carries the optional seed: a zero plan runs the
// whole workflow, while a non-empty plan stamps each seeded task into the fresh
// run record as already-ok and tells the executor to skip them. The store's
// Close error is reported independently so a write failure after a successful
// run does not mask the nil return value.
func runWorkflow(r tui.Renderer, w io.Writer, req runRequest) error {
	// The plan was already printed by the check phase in the caller (doRun /
	// runFromRecord), which validates and prints before any execution. Here we
	// only need the seed material the executor will actually honor.
	rs := resolveSeed(req.wf, req.plan)

	ctx, stop := interruptContext()
	defer stop()

	// home is resolved once by the caller (before any resume-time chdir) so a
	// relative LOOM_HOME cannot split the store across two dirs. Resolve only the
	// invocation cwd here, then thread both into every run record so the store
	// roots under the home directory and a later resume can restore the
	// directory the run was launched from.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	// Load cross-run state once. Each run substitutes `{{state.key}}` from it
	// and folds its `writes_state` outputs back in. A missing file yields an
	// empty map.
	state, err := store.LoadState(req.home, req.wf.ID)
	if err != nil {
		return err
	}

	x := &runExec{r: r, w: w, req: req, state: state}
	_, runErr := x.execute(ctx, cwd, rs)
	return runErr
}

// runExec bundles the context that travels unchanged through the run pipeline's
// execute/finalize hops: the renderer the caller owns, the writer store errors
// go to, the parsed run request, and the cross-run state map (read for
// substitution, written back in place). The per-run locals the executor produces
// as it goes (cwd, the resolved seed, the expected task count) stay as method
// arguments rather than fields, since they are only known partway through.
type runExec struct {
	r     tui.Renderer
	w     io.Writer
	req   runRequest
	state map[string]string
}

// execute executes the DAG: it opens a fresh run record, stamps any seeded
// tasks, runs the executor, prints the summary, and folds `writes_state`
// outputs into state. cwd is the invocation directory the caller resolved (the
// one genuinely new root at this layer); rs is the seed material the run honors.
// The returned report is nil only when the store could not be opened.
func (x *runExec) execute(ctx context.Context, cwd string, rs resolvedSeed) (rep *executor.Report, runErr error) {
	wf := x.req.wf
	run, err := store.Open(wf.ID, x.req.manifest, store.Config{
		Root:        x.req.home,
		Cwd:         cwd,
		OnError:     func(e error) { reportStoreErr(x.w, e) },
		Params:      stringifyParams(x.req.resolved),
		ScheduleID:  x.req.prov.scheduleID,
		TriggeredBy: x.req.prov.triggeredBy,
	})
	if err != nil {
		return nil, err
	}

	// Close is idempotent and must run even if the executor panics, so defer
	// it. rep and runErr are read at defer time, after the executor returns;
	// the closure captures the named returns by reference. The store's Close
	// error is reported but does not mask runErr. The deferred Fprintf has no
	// error channel, so its own write error is intentionally discarded.
	defer func() {
		if closeErr := run.Close(summaryFor(rep), runErr); closeErr != nil {
			reportStoreErr(x.w, closeErr)
		}
	}()

	// The renderer is owned by the caller (doRun / runFromRecord), so execute
	// neither creates nor closes it. Header resets the progress counter and
	// records the denominator.
	expected := len(wf.Tasks) - len(rs.set)
	if err := x.r.Header(tui.RunMeta{
		RunFile: run.Path(),
		Cwd:     cwd,
		Seeded:  len(rs.set),
		Total:   expected,
	}); err != nil {
		return nil, err
	}

	// One store-hooks instance drives both the seeded-task stamping and the
	// executor below, so the run is never double-wrapped.
	sh := storeHooks(run)
	stampSeeded(sh, wf, rs)
	seedExit := seededExitCodes(rs)

	rep, runErr = executor.Run(ctx, wf, executor.JoinHooks(
		x.r.Hooks(),
		sh,
	), executor.Options{Params: x.req.resolved, Seed: rs.seed, SeedExitCodes: seedExit, State: x.state})
	runErr = x.finalize(rep, expected, runErr)
	return rep, runErr
}

// finalize renders the run summary and folds `writes_state` outputs back into
// state once the executor returns. It is a no-op when rep is nil (the store
// failed to open). A summary write error does not mask a real run failure: it is
// surfaced only when the run itself otherwise succeeded, so finalize returns
// the (possibly updated) runErr for the caller to propagate.
func (x *runExec) finalize(rep *executor.Report, expected int, runErr error) error {
	if rep == nil {
		return runErr
	}
	wf := x.req.wf
	if err := x.r.Summary(wf, rep, expected); err != nil && runErr == nil {
		runErr = err
	}
	// Persist write-backs: each task with `writes_state` records its trimmed
	// output under the named key. Only completed tasks appear in rep.Outputs, so
	// a partial run carries over what it managed to produce.
	if persistState(x.state, wf, rep) {
		if err := store.SaveState(x.req.home, wf.ID, x.state); err != nil {
			reportStoreErr(x.w, err)
		}
	}
	return runErr
}

// reportStoreErr writes a store-layer error to w on its own indented line. The
// store is best-effort relative to the run itself, so these writes have no error
// channel of their own and a failed write is intentionally discarded.
func reportStoreErr(w io.Writer, err error) {
	_, _ = fmt.Fprintf(w, "  store: %v\n", err)
}

// persistState folds each `writes_state` task's trimmed output into state and
// reports whether any key changed (so the caller can skip a needless write).
func persistState(state map[string]string, wf *workflow.Workflow, rep *executor.Report) bool {
	changed := false
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if t.WritesState == "" {
			continue
		}
		out, ok := rep.Outputs[t.ID]
		if !ok {
			continue
		}
		state[t.WritesState] = strings.TrimSpace(out)
		changed = true
	}
	return changed
}

// stringifyParams returns nil for an empty bag so `omitempty` keeps params
// absent from the stored JSON rather than writing an empty object.
func stringifyParams(p workflow.ParamValues) map[string]string {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[string(k)] = v
	}
	return out
}

// storeHooks binds store.Run.OnStart and store.Run.OnFinish as method values
// directly: their signatures match executor.Hooks with no adapter needed.
func storeHooks(run *store.Run) executor.Hooks {
	return executor.Hooks{
		OnStart:  run.OnStart,
		OnFinish: run.OnFinish,
	}
}

// summaryFor returns nil when rep is nil so store.Run.Close leaves totals unset.
func summaryFor(rep *executor.Report) *store.Summary {
	if rep == nil {
		return nil
	}
	return &store.Summary{Usage: rep.Usage, TaskCount: len(rep.Tasks)}
}
