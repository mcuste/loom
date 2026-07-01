package executor

import (
	"context"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

func TestProgramPhaseOneSkeleton(t *testing.T) {
	wf := &workflow.Workflow{}
	order := []workflow.TaskID{"build", "test"}
	memberOf := map[workflow.TaskID]int{"loop-body": 0}

	_ = program{
		wf:       wf,
		order:    order,
		units:    []unit{skeletonUnit{}},
		memberOf: memberOf,
	}

	gotTask := taskUnit{id: "build"}
	if gotTask.id != "build" {
		t.Fatalf("taskUnit id = %q, want %q", gotTask.id, "build")
	}

	gotLoop := loopUnit{index: 2}
	if gotLoop.index != 2 {
		t.Fatalf("loopUnit index = %d, want %d", gotLoop.index, 2)
	}
}

type skeletonUnit struct{}

func (skeletonUnit) run(context.Context, *interpreter, *frame) error {
	return nil
}
