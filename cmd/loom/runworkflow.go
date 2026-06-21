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
	id      workflow.TaskID
	prompt  string
	command string
	output  string
}

// resolveSeed reduces a seedPlan to the set and per-task entries the run will
// actually honor, dropping ids no longer present in the current workflow. An
// id that no longer resolves cannot be re-gated, stamped, or skipped, so
// dropping it here keeps the plan annotation, the stamped record, and the
// executor's task count consistent. A zero plan yields nil maps.
func resolveSeed(wf *workflow.Workflow, plan seedPlan) (map[workflow.TaskID]bool, map[workflow.TaskID]seedEntry) {
	if len(plan.seed) == 0 {
		return nil, nil
	}
	set := make(map[workflow.TaskID]bool, len(plan.seed))
	entries := make(map[workflow.TaskID]seedEntry, len(plan.entries))
	for _, s := range plan.entries {
		if wf.ByID(s.id) == nil {
			continue
		}
		entries[s.id] = s
		set[s.id] = true
	}
	return set, entries
}

// runWorkflow is the unified run pipeline shared by doRun and runFromRecord.
// It parses nothing itself; callers hand it the already-parsed workflow and
// resolved params after the check phase has already validated and printed the
// plan. plan carries the optional seed: a zero plan runs the whole workflow,
// while a non-empty plan stamps each seeded task into the fresh run record as
// already-ok and tells the executor to skip them. The store's Close error is
// reported independently so a write failure after a successful run does not
// mask the nil return value.
func runWorkflow(r tui.Renderer, w io.Writer, home string, manifest []byte, wf *workflow.Workflow, resolved workflow.ParamValues, plan seedPlan) error {
	// The plan was already printed by the check phase in the caller (doRun /
	// runFromRecord), which validates and prints before any execution. Here we
	// only need the seeded set the executor will actually honor.
	seededSet, seededEntries := resolveSeed(wf, plan)

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

	// Load cross-run state once. It is the continuity between loop iterations:
	// each iteration substitutes `{{state.key}}` from it and folds its
	// `writes_state` outputs back in. A missing file yields an empty map.
	state, err := store.LoadState(home, wf.ID)
	if err != nil {
		return err
	}

	// A loop runs only on a plain `loom run`. A resume (any seeded task) is
	// single-shot and bypasses the loop wrapper, matching --resume-latest's
	// "resume the last iteration's run" contract.
	if wf.Loop == nil || len(seededSet) > 0 {
		_, runErr := runOnce(ctx, r, w, home, cwd, manifest, wf, resolved, state, plan.seed, seededSet, seededEntries, nil)
		return runErr
	}

	// loop-until-dry: re-run the whole DAG, carrying and flushing state each
	// iteration, until the until_empty task's trimmed output is empty or Max
	// iterations elapse. Each iteration writes its own run record.
	for i := 0; i < wf.Loop.Max; i++ {
		loop := &tui.LoopMeta{N: i + 1, Max: wf.Loop.Max}
		rep, runErr := runOnce(ctx, r, w, home, cwd, manifest, wf, resolved, state, nil, nil, nil, loop)
		if runErr != nil {
			return runErr
		}
		if rep == nil || strings.TrimSpace(rep.Outputs[wf.Loop.UntilEmpty]) == "" {
			break
		}
	}
	return nil
}

// runOnce executes the DAG exactly once: it opens a fresh run record, stamps
// any seeded tasks, runs the executor, prints the summary, and folds
// `writes_state` outputs into state. state is read for substitution and
// written back in place, so the loop wrapper can carry it across iterations.
// The returned report is nil only when the store could not be opened.
func runOnce(ctx context.Context, r tui.Renderer, w io.Writer, home, cwd string, manifest []byte, wf *workflow.Workflow, resolved workflow.ParamValues, state map[string]string, seed map[workflow.TaskID]string, seededSet map[workflow.TaskID]bool, seededEntries map[workflow.TaskID]seedEntry, loop *tui.LoopMeta) (rep *executor.Report, runErr error) {
	run, err := store.Open(wf.ID, manifest, store.Config{
		Root:    home,
		Cwd:     cwd,
		OnError: func(e error) { _, _ = fmt.Fprintf(w, "  store: %v\n", e) },
		Params:  stringifyParams(resolved),
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
			_, _ = fmt.Fprintf(w, "  store: %v\n", closeErr)
		}
	}()

	// The renderer is owned by the caller (doRun / runFromRecord) and shared
	// across loop iterations, so runOnce neither creates nor closes it. Header
	// resets the per-iteration progress counter and records the denominator.
	expected := len(wf.Tasks) - len(seededSet)
	if err := r.Header(tui.RunMeta{
		RunFile: run.Path(),
		Cwd:     cwd,
		Seeded:  len(seededSet),
		Total:   expected,
		Loop:    loop,
	}); err != nil {
		return nil, err
	}

	// Stamp each seeded task into the new run record as an already-ok entry so
	// a future resume of this run can find them. The executor fires no hooks
	// for seeded tasks (by design), so drive the store hooks directly here,
	// mimicking what the executor would have done. The same Hooks instance is
	// reused for the executor below so the run is never double-wrapped.
	// Suppressed entirely on a plain run so its output stays byte-identical.
	sh := storeHooks(run)
	if len(seededSet) > 0 {
		// Stamp in topological (plan) order so a seeded task is never recorded
		// before a seeded dependency it relies on.
		for _, id := range wf.Plan() {
			s, ok := seededEntries[id]
			if !ok {
				continue
			}
			t := wf.ByID(id)
			if t == nil {
				continue
			}
			if t.IsShell() {
				sh.OnStart(*t, "", "", "")
			} else {
				rt, m, e := wf.Effective(t)
				sh.OnStart(*t, rt, m, e)
			}
			sh.OnFinish(*t, executor.TaskResult{
				TaskID:  id,
				Prompt:  s.prompt,
				Command: s.command,
				Output:  s.output,
			}, nil)
		}
	}

	rep, runErr = executor.Run(ctx, wf, executor.JoinHooks(
		r.Hooks(),
		sh,
	), executor.Options{Params: resolved, Seed: seed, State: state})
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
			if err := store.SaveState(home, wf.ID, state); err != nil {
				_, _ = fmt.Fprintf(w, "  store: %v\n", err)
			}
		}
	}
	return rep, runErr
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
