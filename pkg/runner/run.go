package runner

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// Provenance records what initiated a run so the store can distinguish a
// scheduled run from a direct CLI invocation. A zero value marks a direct CLI
// run; only the daemon supplies one.
type Provenance struct {
	ScheduleID  string
	TriggeredBy string
}

// Request bundles everything the unified run pipeline consumes: the parsed
// workflow and its inlined manifest, the resolved params, the resolved
// LOOM_HOME, the invocation working directory, the optional seed plan, and the
// provenance. Wf, Manifest, and Resolved arrive together from the check phase,
// so grouping them keeps the pipeline signature stable. A zero Plan and zero
// Prov mark a plain direct run. Cwd is the directory the run is launched from;
// it is stored in the run record so a later resume can restore it.
type Request struct {
	Wf       *workflow.Workflow
	Manifest []byte
	Resolved workflow.ParamValues
	Home     string
	Cwd      string
	Plan     SeedPlan
	Prov     Provenance
}

// Run is the unified run pipeline shared by doRun, runFromRecord, and the
// daemon. It parses nothing itself; callers hand it the already-parsed workflow
// and resolved params after the check phase has already validated and printed
// the plan. req.Plan carries the optional seed: a zero plan runs the whole
// workflow, while a non-empty plan stamps each seeded task into the fresh run
// record as already-ok and tells the executor to skip them. The store's Close
// error is reported independently so a write failure after a successful run does
// not mask the nil return value. ctx should be a signal-aware context created by
// the caller; Run cancels execution on ctx cancellation.
func Run(ctx context.Context, r tui.Renderer, w io.Writer, req Request) (string, error) {
	// The plan was already printed by the check phase in the caller (doRun /
	// runFromRecord), which validates and prints before any execution. Here we
	// only need the seed material the executor will actually honor.
	rs := resolveSeed(req.Wf, req.Plan)

	// Home is resolved once by the caller (before any resume-time chdir) so a
	// relative LOOM_HOME cannot split the store across two dirs. Cwd is supplied
	// by the caller so it reflects the directory the run was launched from (not
	// the directory Run executes in, which may differ after a resume-time chdir).
	cwd := req.Cwd

	// Load cross-run state once. Each run substitutes `{{state.key}}` from it
	// and folds its `writes_state` outputs back in. A missing file yields an
	// empty map.
	state, err := store.LoadState(req.Home, req.Wf.ID)
	if err != nil {
		return "", err
	}

	x := &runExec{r: r, w: w, req: req, state: state}
	return x.execute(ctx, cwd, rs)
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
	req   Request
	state map[string]string
}

// execute executes the DAG: it opens a fresh run record, stamps any seeded
// tasks, runs the executor, prints the summary, and folds `writes_state`
// outputs into state. cwd is the invocation directory the caller resolved (the
// one genuinely new root at this layer); rs is the seed material the run honors.
func (x *runExec) execute(ctx context.Context, cwd string, rs resolvedSeed) (runID string, runErr error) {
	wf := x.req.Wf
	run, err := store.Open(wf.ID, x.req.Manifest, store.Config{
		Root:        x.req.Home,
		Cwd:         cwd,
		OnError:     func(e error) { reportStoreErr(x.w, e) },
		Params:      stringifyParams(x.req.Resolved),
		ScheduleID:  x.req.Prov.ScheduleID,
		TriggeredBy: x.req.Prov.TriggeredBy,
	})
	if err != nil {
		return "", err
	}
	runID = run.ID()

	// Close is idempotent and must run even if the executor panics, so defer
	// it. rep and runErr are read at defer time, after the executor returns:
	// rep is a local the closure captures, runErr the named return. The store's
	// Close error is reported but does not mask runErr. The deferred Fprintf has
	// no error channel, so its own write error is intentionally discarded.
	var rep *executor.Report
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
		return runID, err
	}

	// One store-hooks instance drives both the seeded-task stamping and the
	// executor below, so the run is never double-wrapped.
	sh := storeHooks(run)
	stampSeeded(sh, wf, rs)
	seedExit := seededExitCodes(rs)

	rep, runErr = executor.Run(ctx, wf, executor.JoinHooks(
		x.r.Hooks(),
		sh,
	), executor.Options{Params: x.req.Resolved, Seed: rs.seed, SeedExitCodes: seedExit, State: x.state})
	runErr = x.finalize(rep, expected, runErr)
	return runID, runErr
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
	wf := x.req.Wf
	if err := x.r.Summary(wf, rep, expected); err != nil && runErr == nil {
		runErr = err
	}
	// Persist write-backs: each task with `writes_state` records its trimmed
	// output under the named key. Only completed tasks appear in rep.Outputs, so
	// a partial run carries over what it managed to produce.
	if persistState(x.state, wf, rep) {
		if err := store.SaveState(x.req.Home, wf.ID, x.state); err != nil {
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

// toRecord maps an executor.TaskResult to the store.TaskRecord DTO that
// Run.OnFinish consumes. The translation lives here so pkg/store need not
// import pkg/executor.
func toRecord(res executor.TaskResult) store.TaskRecord {
	return store.NewTaskRecord(
		res.Prompt,
		res.Command,
		res.Output,
		res.ExitCode,
		res.Elapsed,
		string(res.Status),
		res.Usage,
	)
}

// storeHooks binds store.Run.OnStart as a direct method value and wraps
// OnFinish with toRecord so the executor's TaskResult is converted before it
// reaches the store.
func storeHooks(run *store.Run) executor.Hooks {
	return executor.Hooks{
		OnStart: run.OnStart,
		OnFinish: func(t workflow.Task, iter int, res executor.TaskResult, err error) {
			run.OnFinish(t, iter, toRecord(res), err)
		},
	}
}

// summaryFor returns nil when rep is nil so store.Run.Close leaves totals unset.
func summaryFor(rep *executor.Report) *store.Summary {
	if rep == nil {
		return nil
	}
	return &store.Summary{Usage: rep.Usage, TaskCount: len(rep.Tasks)}
}
