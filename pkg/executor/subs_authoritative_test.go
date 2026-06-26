package executor_test

import (
	"context"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestRunReadsSubsFromWorkflow pins that wf.Subs is the authoritative source of
// sub-workflow links: a parent whose Subs holds the child resolves and runs that
// child even when the caller leaves Options.Subs empty. Callers should not have
// to copy wf.Subs into Options to make dispatch work; the executor already holds
// the parent workflow and must read its links directly.
func TestRunReadsSubsFromWorkflow(t *testing.T) {
	child := releaseChild()
	parent := &workflow.Workflow{
		ID:      "parent",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "cut", Workflow: "release", With: []workflow.WithArg{{Name: "version", Value: "1.4.0"}}},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": child},
	}

	rep, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = "publish build 1.4.0"
	if rep.Outputs["cut"] != want {
		t.Errorf("Outputs[cut] = %q, want %q (child resolved from wf.Subs without Options.Subs)", rep.Outputs["cut"], want)
	}
}

// TestRunReadsNestedSubsFromWorkflow pins that the authoritative-Subs contract
// holds across nesting: each level resolves its own wf.Subs, so a grandparent
// linking a mid workflow that links the leaf child runs end to end with an empty
// Options.Subs at the top call.
func TestRunReadsNestedSubsFromWorkflow(t *testing.T) {
	leaf := releaseChild()
	mid := &workflow.Workflow{
		ID:      "mid",
		Runtime: "exec-echo",
		Model:   "m1",
		Output:  "cut",
		Params:  []workflow.Param{{Name: "version", Required: true}},
		Tasks: []workflow.Task{
			{ID: "cut", Workflow: "release", With: []workflow.WithArg{{Name: "version", Value: "{{params.version}}"}}},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"cut": leaf},
	}
	top := &workflow.Workflow{
		ID:      "top",
		Runtime: "exec-echo",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "outer", Workflow: "mid", With: []workflow.WithArg{{Name: "version", Value: "3.3.3"}}},
		},
		Subs: map[workflow.TaskID]*workflow.Workflow{"outer": mid},
	}

	rep, err := executor.Run(context.Background(), top, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["outer"] != "publish build 3.3.3" {
		t.Errorf("Outputs[outer] = %q, want %q (leaf resolved from per-level wf.Subs)", rep.Outputs["outer"], "publish build 3.3.3")
	}
}
