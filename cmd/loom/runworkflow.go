package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// seedPlan carries the optional seeded-task set threaded into the unified run
// pipeline. seed maps task ids to their stored output (fed to
// executor.Options.Seed and used to annotate the printed plan); entries holds
// the per-seeded-task material the store needs to stamp a synthetic ok record
// before the executor starts. A zero seedPlan (nil seed, nil entries) marks a
// plain run, where the Seeded line and the seed-stamping are both suppressed.
type seedPlan struct {
	seed    map[workflow.TaskID]string
	entries []seedEntry
}

// seedEntry is the per-task material stamped into a fresh run record for a
// task whose output is being carried over from a prior run. Fields are
// unexported: the type is package-internal and only assembled by callers in
// this package.
type seedEntry struct {
	id       workflow.TaskID
	prompt   string
	command  string
	output   string
	exitCode int
}

// resolvedSeed is the seed material a run actually honors. seed is the per-task
// output fed verbatim to executor.Options.Seed; set is the id set the executor
// skips and the plan annotates; entries is the per-task material stamped into
// the fresh run record. set and entries are filtered to ids still present in the
// current workflow, while seed is carried verbatim. A zero value (all nil) marks
// a plain run.
type resolvedSeed struct {
	seed    map[workflow.TaskID]string
	set     map[workflow.TaskID]bool
	entries map[workflow.TaskID]seedEntry
}

// resolveSeed reduces a seedPlan to the seed material the run will actually
// honor, dropping ids no longer present in the current workflow from set and
// entries. An id that no longer resolves cannot be re-gated, stamped, or
// skipped, so dropping it here keeps the plan annotation, the stamped record,
// and the executor's task count consistent. A zero plan yields a zero
// resolvedSeed.
func resolveSeed(wf *workflow.Workflow, plan seedPlan) resolvedSeed {
	if len(plan.seed) == 0 {
		return resolvedSeed{}
	}
	rs := resolvedSeed{
		seed:    plan.seed,
		set:     make(map[workflow.TaskID]bool, len(plan.seed)),
		entries: make(map[workflow.TaskID]seedEntry, len(plan.entries)),
	}
	for _, s := range plan.entries {
		if wf.ByID(s.id) == nil {
			continue
		}
		rs.entries[s.id] = s
		rs.set[s.id] = true
	}
	return rs
}

// runContext carries the on-disk roots a single run is recorded against: the
// resolved LOOM_HOME, the cwd the run was launched from, and the inlined
// manifest the store persists.
type runContext struct {
	home     string
	cwd      string
	manifest []byte
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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

	rc := runContext{home: req.home, cwd: cwd, manifest: req.manifest}
	_, runErr := runOnce(ctx, r, w, rc, req.wf, req.resolved, state, rs, req.prov)
	return runErr
}

// runOnce executes the DAG exactly once: it opens a fresh run record, stamps
// any seeded tasks, runs the executor, prints the summary, and folds
// `writes_state` outputs into state. state is read for substitution and
// written back in place.
// The returned report is nil only when the store could not be opened.
func runOnce(ctx context.Context, r tui.Renderer, w io.Writer, rc runContext, wf *workflow.Workflow, resolved workflow.ParamValues, state map[string]string, rs resolvedSeed, prov provenance) (rep *executor.Report, runErr error) {
	run, err := store.Open(wf.ID, rc.manifest, store.Config{
		Root:        rc.home,
		Cwd:         rc.cwd,
		OnError:     func(e error) { reportStoreErr(w, e) },
		Params:      stringifyParams(resolved),
		ScheduleID:  prov.scheduleID,
		TriggeredBy: prov.triggeredBy,
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
			reportStoreErr(w, closeErr)
		}
	}()

	// The renderer is owned by the caller (doRun / runFromRecord), so runOnce
	// neither creates nor closes it. Header resets the progress counter and
	// records the denominator.
	expected := len(wf.Tasks) - len(rs.set)
	if err := r.Header(tui.RunMeta{
		RunFile: run.Path(),
		Cwd:     rc.cwd,
		Seeded:  len(rs.set),
		Total:   expected,
	}); err != nil {
		return nil, err
	}

	// One store-hooks instance drives both the seeded-task stamping and the
	// executor below, so the run is never double-wrapped.
	sh := storeHooks(run)
	seedExit := stampSeeded(sh, wf, rs)

	rep, runErr = executor.Run(ctx, wf, executor.JoinHooks(
		r.Hooks(),
		sh,
	), executor.Options{Params: resolved, Seed: rs.seed, SeedExitCodes: seedExit, State: state})
	if rep != nil {
		// A summary write error does not mask a real run failure: surface it only
		// when the run itself otherwise succeeded.
		if err := r.Summary(wf, rep, expected); err != nil && runErr == nil {
			runErr = err
		}
		// Persist write-backs: each task with `writes_state` records its trimmed
		// output under the named key. Only completed tasks appear in
		// rep.Outputs, so a partial run carries over what it managed to produce.
		if persistState(state, wf, rep) {
			if err := store.SaveState(rc.home, wf.ID, state); err != nil {
				reportStoreErr(w, err)
			}
		}
	}
	return rep, runErr
}

// stampSeeded records each seeded task into the new run record as an already-ok
// entry (via the store hooks sh) so a future resume of this run can find them.
// The executor fires no hooks for seeded tasks by design, so this mimics what it
// would have done. It returns the per-task exit codes to feed the executor as
// SeedExitCodes; both are empty on a plain run, so its output stays
// byte-identical. sh is the same hooks instance the executor reuses, so the run
// is never double-wrapped.
func stampSeeded(sh executor.Hooks, wf *workflow.Workflow, rs resolvedSeed) map[workflow.TaskID]int {
	// Stamp in topological (plan) order so a seeded task is never recorded
	// before a seeded dependency it relies on.
	for _, id := range wf.Plan() {
		s, ok := rs.entries[id]
		if !ok {
			continue
		}
		t := wf.ByID(id)
		if t == nil {
			continue
		}
		if t.IsShell() || t.IsSubWorkflow() || t.IsScript() {
			// Shell, script, and sub-workflow tasks have no runtime of their own
			// (a sub-workflow's child brings its own), so they seed with empty
			// runtime metadata, matching the ("", "", "") OnStart that runTask
			// fires for them.
			sh.OnStart(*t, 0, "", "", "")
		} else {
			rt, m, e := wf.Effective(t)
			sh.OnStart(*t, 0, rt, m, e)
		}
		sh.OnFinish(*t, 0, executor.TaskResult{
			TaskID:   id,
			Prompt:   s.prompt,
			Command:  s.command,
			Output:   s.output,
			ExitCode: s.exitCode,
		}, nil)
	}

	// Carry seeded exit codes into the run so a resumed downstream task's
	// `{{id.exit}}` resolves to the recorded code. Every seeded task is recorded
	// (not just non-zero ones), mirroring a fresh run where recordResult stores an
	// exit code for every completed task: a seeded clean-exit script must resolve
	// to "0", not be left verbatim as an unknown reference.
	if len(rs.entries) == 0 {
		return nil
	}
	seedExit := make(map[workflow.TaskID]int, len(rs.entries))
	for id, s := range rs.entries {
		seedExit[id] = s.exitCode
	}
	return seedExit
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
