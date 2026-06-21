package executor

import (
	"context"
	"sync"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// newTrivialState builds a runState for a single task with one open gate, the
// shape Run sets up before launching a goroutine. It is the minimal fixture
// runTask needs for a budget-free, condition-free task.
func newTrivialState(id workflow.TaskID) *runState {
	var mu sync.Mutex
	return &runState{
		rep: &Report{
			Tasks:   make([]TaskResult, 0),
			Outputs: make(map[workflow.TaskID]string),
		},
		succeeded:   make(map[workflow.TaskID]bool),
		skipped:     make(map[workflow.TaskID]bool),
		gates:       map[workflow.TaskID]chan struct{}{id: make(chan struct{})},
		mu:          &mu,
		budgetReady: sync.NewCond(&mu),
	}
}

// TestRunTask pins that runTask, given a trivial shell task, substitutes and
// dispatches it, then on success records the output into the shared report
// (both Outputs[id] and an appended Tasks entry) and closes the task's gate so
// downstream waiters proceed, just as Run's inline body does. The subtests
// share one runTask invocation so `echo hi` runs once.
func TestRunTask(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		ID:    "wf",
		Tasks: []workflow.Task{{ID: "a", Command: "echo hi"}},
	}
	st := newTrivialState("a")

	if err := runTask(context.Background(), wf, wf.ByID("a"), st, Hooks{}, Options{}); err != nil {
		t.Fatalf("runTask: %v", err)
	}

	t.Run("records output", func(t *testing.T) {
		if got := st.rep.Outputs["a"]; got != "hi" {
			t.Errorf("Outputs[a] = %q, want %q", got, "hi")
		}
	})

	t.Run("appends task to report", func(t *testing.T) {
		if len(st.rep.Tasks) != 1 {
			t.Fatalf("len(Tasks) = %d, want 1", len(st.rep.Tasks))
		}
		if got := st.rep.Tasks[0]; got.TaskID != "a" || got.Output != "hi" || got.Status != StatusOK {
			t.Errorf("Tasks[0] = %+v, want {TaskID:a Output:hi Status:%s}", got, StatusOK)
		}
	})

	t.Run("closes gate", func(t *testing.T) {
		select {
		case <-st.gates["a"]:
			// gate closed, as required
		default:
			t.Errorf("gate for task %q not closed", workflow.TaskID("a"))
		}
	})
}
