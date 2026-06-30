package main

import (
	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// seedPlan carries the optional seeded tasks threaded into the unified run
// pipeline. Each entry holds the per-task material a resume carries over from a
// prior run: the stored output (fed to executor.Options.Seed and used to
// annotate the printed plan) plus the metadata the store needs to stamp a
// synthetic ok record before the executor starts. A zero seedPlan (nil entries)
// marks a plain run, where the Seeded line and the seed-stamping are both
// suppressed.
type seedPlan struct {
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

// seedPlanFromRecord builds the carry-over plan from a prior run's ok tasks.
// It is the one seedEntry-building site: every ok task in the record becomes an
// entry unfiltered, leaving resolveSeed as the single authority that later drops
// ids no longer present in the current workflow.
func seedPlanFromRecord(rec *store.RunRecord) seedPlan {
	var plan seedPlan
	for _, t := range rec.Tasks {
		if t.Status != store.StatusOK {
			continue
		}
		plan.entries = append(plan.entries, seedEntry{
			id:       workflow.TaskID(t.ID),
			prompt:   t.Prompt,
			command:  t.Command,
			output:   t.Output,
			exitCode: t.ExitCode,
		})
	}
	return plan
}

// resolvedSeed is the seed material a run actually honors, all of it filtered to
// ids still present in the current workflow. seed is the per-task output fed to
// executor.Options.Seed; set is the id set the executor skips and the plan
// annotates; entries is the per-task material stamped into the fresh run record.
// A zero value (all nil) marks a plain run.
type resolvedSeed struct {
	seed    map[workflow.TaskID]string
	set     map[workflow.TaskID]bool
	entries map[workflow.TaskID]seedEntry
}

// resolveSeed reduces a seedPlan to the seed material the run will actually
// honor, dropping ids no longer present in the current workflow. It is the
// single filtering authority: an id that no longer resolves cannot be re-gated,
// stamped, or skipped, so dropping it here keeps the plan annotation, the
// stamped record, the executor's seed map, and its task count consistent. The
// executor ignores Seed keys with no matching task, so callers hand resolveSeed
// every prior-run task unfiltered and let it decide. A zero plan yields a zero
// resolvedSeed.
func resolveSeed(wf *workflow.Workflow, plan seedPlan) resolvedSeed {
	if len(plan.entries) == 0 {
		return resolvedSeed{}
	}
	rs := resolvedSeed{
		seed:    make(map[workflow.TaskID]string, len(plan.entries)),
		set:     make(map[workflow.TaskID]bool, len(plan.entries)),
		entries: make(map[workflow.TaskID]seedEntry, len(plan.entries)),
	}
	for _, s := range plan.entries {
		if wf.ByID(s.id) == nil {
			continue
		}
		rs.seed[s.id] = s.output
		rs.entries[s.id] = s
		rs.set[s.id] = true
	}
	return rs
}

// stampSeeded records each seeded task into the new run record as an already-ok
// entry (via the store hooks sh) so a future resume of this run can find them.
// The executor fires no hooks for seeded tasks by design, so this mimics what it
// would have done. On a plain run rs.entries is empty and nothing is stamped, so
// the output stays byte-identical. sh is the same hooks instance the executor
// reuses, so the run is never double-wrapped.
func stampSeeded(sh executor.Hooks, wf *workflow.Workflow, rs resolvedSeed) {
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
		// StartMeta is the single authority for which task kinds carry runtime
		// metadata: a prompt task reports its effective triple, every other kind
		// the empty ("", "", "") triple, matching the OnStart runTask fires.
		rt, m, e := wf.StartMeta(t)
		sh.OnStart(*t, 0, rt, m, e)
		sh.OnFinish(*t, 0, executor.TaskResult{
			TaskID:   id,
			Prompt:   s.prompt,
			Command:  s.command,
			Output:   s.output,
			ExitCode: s.exitCode,
		}, nil)
	}
}

// seededExitCodes returns the per-seeded-task exit codes to feed the executor as
// SeedExitCodes, so a resumed downstream task's `{{id.exit}}` resolves to the
// recorded code. Every seeded task is recorded (not just non-zero ones),
// mirroring a fresh run where recordResult stores an exit code for every
// completed task: a seeded clean-exit script must resolve to "0", not be left
// verbatim as an unknown reference. It returns nil on a plain run so the
// executor's output stays byte-identical.
func seededExitCodes(rs resolvedSeed) map[workflow.TaskID]int {
	if len(rs.entries) == 0 {
		return nil
	}
	seedExit := make(map[workflow.TaskID]int, len(rs.entries))
	for id, s := range rs.entries {
		seedExit[id] = s.exitCode
	}
	return seedExit
}
