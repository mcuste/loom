package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/store"
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

// runWorkflow is the unified run pipeline shared by doRun and runFromRecord.
// It parses nothing itself; callers hand it the already-parsed workflow,
// resolved params, and CLI param map (for plan provenance). plan carries the
// optional seed: a zero plan runs the whole workflow, while a non-empty plan
// annotates the printed plan, stamps each seeded task into the fresh run
// record as already-ok, and tells the executor to skip them. The store's
// Close error is reported independently so a write failure after a successful
// run does not mask the nil return value.
func runWorkflow(w io.Writer, manifest []byte, wf *workflow.Workflow, resolved workflow.ParamValues, cliParams map[string]string, plan seedPlan) error {
	// Index seeded entries by id, keeping only those still present in the
	// current workflow. An id that no longer resolves cannot be re-gated,
	// stamped, or skipped, so dropping it here keeps the plan annotation, the
	// stamped record, and the expected-task count consistent with what the
	// executor will actually do.
	var (
		seededSet     map[workflow.TaskID]bool
		seededEntries map[workflow.TaskID]seedEntry
	)
	if len(plan.seed) > 0 {
		seededSet = make(map[workflow.TaskID]bool, len(plan.seed))
		seededEntries = make(map[workflow.TaskID]seedEntry, len(plan.entries))
		for _, s := range plan.entries {
			if wf.ByID(s.id) == nil {
				continue
			}
			seededEntries[s.id] = s
			seededSet[s.id] = true
		}
	}

	printPlan(w, wf, resolved, cliParams, seededSet)
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	run, err := store.Open(wf.ID, manifest, store.Config{
		OnError: func(e error) { fmt.Fprintf(w, "  store: %v\n", e) },
		Params:  stringifyParams(resolved),
	})
	if err != nil {
		return err
	}

	// Close is idempotent and must run even if the executor panics, so defer
	// it. rep and runErr are read at defer time, after the executor returns;
	// the closure captures them by reference. The store's Close error is
	// reported but does not mask runErr.
	var (
		rep    *executor.Report
		runErr error
	)
	defer func() {
		if closeErr := run.Close(summaryFor(rep), runErr); closeErr != nil {
			fmt.Fprintf(w, "  store: %v\n", closeErr)
		}
	}()

	fmt.Fprintf(w, "Run file : %s\n\n", run.Path())

	// Stamp each seeded task into the new run record as an already-ok entry so
	// a future resume of this run can find them. The executor fires no hooks
	// for seeded tasks (by design), so drive the store hooks directly here,
	// mimicking what the executor would have done. The same Hooks instance is
	// reused for the executor below so the run is never double-wrapped.
	// Suppressed entirely on a plain run so its output stays byte-identical.
	sh := storeHooks(run)
	if len(seededSet) > 0 {
		fmt.Fprintf(w, "Seeded   : %d task(s) from prior run\n\n", len(seededSet))

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

	expected := len(wf.Tasks) - len(seededSet)
	rep, runErr = executor.Run(ctx, wf, executor.JoinHooks(
		hooks(w, expected),
		sh,
	), executor.Options{Params: resolved, Seed: plan.seed})
	if rep != nil {
		printSummary(w, wf, rep, expected)
	}
	return runErr
}
