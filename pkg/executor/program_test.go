package executor

import (
	"context"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/plan"
	"github.com/mcuste/loom/pkg/workflow"
)

func TestProgramPhaseOneSkeleton(t *testing.T) {
	wf := &workflow.Workflow{}
	pl := &plan.Plan{Order: []plan.StepID{"build", "test"}}
	memberOf := map[workflow.TaskID]int{"loop-body": 0}

	prog := program{
		wf:       wf,
		plan:     pl,
		units:    []unit{skeletonUnit{}},
		memberOf: memberOf,
	}
	if !slices.Equal(prog.order(), []workflow.TaskID{"build", "test"}) {
		t.Fatalf("program order = %v, want [build test]", prog.order())
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
