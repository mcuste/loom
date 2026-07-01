package run

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/task"
	"github.com/mcuste/loom/pkg/workflow"
)

func TestHooksFromSinkMirrorsStartFinishAndSkip(t *testing.T) {
	t.Parallel()

	var events []Event
	hooks := HooksFromSink(EventSinkFunc(func(e Event) {
		events = append(events, e)
	}))

	taskA := workflow.Task{ID: "a"}
	hooks.OnStart(taskA, 2, "rt", "model", "effort")
	errBoom := errors.New("boom")
	hooks.OnFinish(taskA, 2, executor.TaskResult{
		TaskID:    "a",
		Output:    "done",
		Status:    executor.StatusOK,
		Iteration: 2,
	}, errBoom)

	taskB := workflow.Task{ID: "b"}
	hooks.OnFinish(taskB, 0, executor.TaskResult{
		TaskID: "b",
		Status: task.StatusSkipped,
	}, nil)

	if len(events) != 4 {
		t.Fatalf("events len = %d, want 4: %#v", len(events), events)
	}
	start, ok := events[0].(StepStarted)
	if !ok {
		t.Fatalf("events[0] = %T, want StepStarted", events[0])
	}
	if start.ID != "a" || start.Iteration != 2 || start.Runtime != "rt" || start.Model != "model" || start.Effort != "effort" {
		t.Fatalf("StepStarted = %#v", start)
	}
	finished, ok := events[1].(StepFinished)
	if !ok {
		t.Fatalf("events[1] = %T, want StepFinished", events[1])
	}
	if finished.ID != "a" || finished.Result.Output != "done" || !errors.Is(finished.Err, errBoom) {
		t.Fatalf("StepFinished = %#v", finished)
	}
	if skipped, ok := events[2].(StepSkipped); !ok || skipped.ID != "b" {
		t.Fatalf("events[2] = %#v, want StepSkipped for b", events[2])
	}
	if finished, ok := events[3].(StepFinished); !ok || finished.ID != "b" || finished.Result.Status != task.StatusSkipped {
		t.Fatalf("events[3] = %#v, want StepFinished skipped b", events[3])
	}
}
