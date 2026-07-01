package plan

import (
	"reflect"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

func TestCompilePreservesWorkflowPlanOrder(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "c", Prompt: "c", DependsOn: []workflow.TaskID{"b"}},
			{ID: "a", Prompt: "a"},
			{ID: "b", Prompt: "b", DependsOn: []workflow.TaskID{"a"}},
		},
	}
	pl, err := Compile(wf, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	want := []StepID{"a", "b", "c"}
	if !reflect.DeepEqual(pl.Order, want) {
		t.Fatalf("Order = %v, want %v", pl.Order, want)
	}
}

func TestCompileLowersLoopToRepeatBlock(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "seed", Prompt: "seed"},
			{ID: "member", Prompt: "member", DependsOn: []workflow.TaskID{"seed"}},
			{ID: "after", Prompt: "after", DependsOn: []workflow.TaskID{"member"}},
		},
		Loops: []workflow.LoopGroup{{
			ID:         "loop",
			Members:    []workflow.TaskID{"member"},
			UntilEmpty: "member",
			Max:        3,
		}},
	}
	pl, err := Compile(wf, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	loopStep := pl.Steps["loop"]
	if _, ok := loopStep.Action.(Repeat); !ok {
		t.Fatalf("loop action = %T, want Repeat", loopStep.Action)
	}
	if !reflect.DeepEqual(loopStep.Deps, []StepID{"seed"}) {
		t.Fatalf("loop deps = %v, want [seed]", loopStep.Deps)
	}
	if !reflect.DeepEqual(pl.Blocks["loop"].Steps, []StepID{"member"}) {
		t.Fatalf("loop block steps = %v, want [member]", pl.Blocks["loop"].Steps)
	}
}

func TestCompileLowersForEachConcurrency(t *testing.T) {
	t.Parallel()

	wf := &workflow.Workflow{
		Tasks: []workflow.Task{
			{ID: "discover", Prompt: "items"},
			{ID: "member", Prompt: "{{item}}"},
		},
		Loops: []workflow.LoopGroup{{
			ID:         "fan",
			Kind:       workflow.LoopForEach,
			Parallel:   true,
			ListSource: "{{discover}}",
			As:         "item",
			Members:    []workflow.TaskID{"member"},
		}},
	}
	pl, err := Compile(wf, CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	action, ok := pl.Steps["fan"].Action.(ForEach)
	if !ok {
		t.Fatalf("fan action = %T, want ForEach", pl.Steps["fan"].Action)
	}
	if action.Concurrency != 0 {
		t.Fatalf("Concurrency = %d, want 0 for max", action.Concurrency)
	}
	if !reflect.DeepEqual(pl.Steps["fan"].Deps, []StepID{"discover"}) {
		t.Fatalf("fan deps = %v, want [discover]", pl.Steps["fan"].Deps)
	}
}
