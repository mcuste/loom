package workflow_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestOutputTaskExplicit pins that an explicit Output wins: OutputTask returns
// it even when another task would be the lone sink.
func TestOutputTaskExplicit(t *testing.T) {
	wf := &workflow.Workflow{
		ID:     "wf",
		Output: "build",
		Tasks: []workflow.Task{
			{ID: "build", Prompt: "b"},
			{ID: "publish", Prompt: "p {{build}}", DependsOn: []workflow.TaskID{"build"}},
		},
	}
	got, err := wf.OutputTask()
	if err != nil {
		t.Fatalf("OutputTask returned error: %v", err)
	}
	if got != "build" {
		t.Errorf("OutputTask = %q, want %q (explicit output wins over sink)", got, "build")
	}
}

// TestOutputTaskExplicitUnknown pins that an explicit Output naming a task that
// does not exist is an error.
func TestOutputTaskExplicitUnknown(t *testing.T) {
	wf := &workflow.Workflow{
		ID:     "wf",
		Output: "ghost",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "a"},
		},
	}
	if _, err := wf.OutputTask(); err == nil {
		t.Fatal("OutputTask returned nil error for an unknown Output; want error")
	}
}

// TestOutputTaskSingleSinkDefault pins the default: with no explicit Output,
// OutputTask returns the lone sink (the task no other task depends on).
func TestOutputTaskSingleSinkDefault(t *testing.T) {
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "build", Prompt: "b"},
			{ID: "publish", Prompt: "p {{build}}", DependsOn: []workflow.TaskID{"build"}},
		},
	}
	got, err := wf.OutputTask()
	if err != nil {
		t.Fatalf("OutputTask returned error: %v", err)
	}
	if got != "publish" {
		t.Errorf("OutputTask = %q, want %q (lone sink default)", got, "publish")
	}
}

// TestOutputTaskMultipleSinks pins that two terminal tasks with no explicit
// Output is an error: the result is ambiguous.
func TestOutputTaskMultipleSinks(t *testing.T) {
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "a"},
			{ID: "b", Prompt: "b"},
		},
	}
	if _, err := wf.OutputTask(); err == nil {
		t.Fatal("OutputTask returned nil error for multiple sinks; want ambiguity error")
	}
}

// TestOutputTaskZeroSinks pins that a graph with no sink (every task is depended
// upon) and no explicit Output is an error.
func TestOutputTaskZeroSinks(t *testing.T) {
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "a", DependsOn: []workflow.TaskID{"b"}},
			{ID: "b", Prompt: "b", DependsOn: []workflow.TaskID{"a"}},
		},
	}
	if _, err := wf.OutputTask(); err == nil {
		t.Fatal("OutputTask returned nil error for zero sinks; want error")
	}
}
