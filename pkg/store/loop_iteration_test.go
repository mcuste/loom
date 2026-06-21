package store_test

import (
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestRunRecord_TwoIterationLoopRoundTrips pins that a looped task's per-pass
// results are persisted as distinct records rather than collapsing onto one
// entry: driving OnStart/OnFinish for the same task id across two iterations,
// then Load-ing the saved run, must recover both passes with their iteration
// indices and outputs intact.
func TestRunRecord_TwoIterationLoopRoundTrips(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{
		Root: root,
		Now:  fixedClock("2026-06-09T14:30:52Z"),
		Rand: counterRand(0xa1b2c3),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	task := workflow.Task{ID: "draft", Prompt: "p"}
	// Pass 1 and pass 2 of the same looped task: same id, distinct iteration and
	// output. Both must survive the round-trip.
	run.OnStart(task, 1, "claude-code", "sonnet", "low")
	run.OnFinish(task, 1, executor.TaskResult{Output: "first", Iteration: 1}, nil)
	run.OnStart(task, 2, "claude-code", "sonnet", "low")
	run.OnFinish(task, 2, executor.TaskResult{Output: "second", Iteration: 2}, nil)

	if err := run.Close(&store.Summary{}, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec, err := store.Load(run.Path())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var iters []int
	outByIter := map[int]string{}
	for _, tr := range rec.Tasks {
		if tr.ID != "draft" {
			continue
		}
		iters = append(iters, tr.Iteration)
		outByIter[tr.Iteration] = tr.Output
	}
	slices.Sort(iters)

	if want := []int{1, 2}; !slices.Equal(iters, want) {
		t.Fatalf("draft iterations = %v, want %v (each pass its own record)", iters, want)
	}
	if outByIter[1] != "first" || outByIter[2] != "second" {
		t.Errorf("outputs by iteration = %v, want {1:first 2:second}", outByIter)
	}
}
