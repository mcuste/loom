package workflow_test

import (
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

func TestParseDefinitionReturnsSemanticModel(t *testing.T) {
	src := []byte(`
name: wf
runtime: test-rt
model: m1
output: finish
tasks:
  - id: first
    prompt: first
  - id: finish
    depends_on: [first]
    prompt: "finish {{first}}"
`)
	def, err := workflow.ParseDefinition(src)
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}

	if def.ID != "wf" {
		t.Fatalf("definition ID = %q, want wf", def.ID)
	}
	if def.Output.Task != "finish" {
		t.Fatalf("definition output = %q, want finish", def.Output.Task)
	}
	if !slices.Equal(def.Order, []workflow.TaskID{"first", "finish"}) {
		t.Fatalf("definition order = %v, want [first finish]", def.Order)
	}
	if len(def.Nodes) != 2 {
		t.Fatalf("definition nodes = %d, want 2", len(def.Nodes))
	}
	finish, ok := def.Nodes[1].(workflow.TaskNode)
	if !ok {
		t.Fatalf("second node = %T, want workflow.TaskNode", def.Nodes[1])
	}
	if _, ok := finish.Action.(workflow.PromptAction); !ok {
		t.Fatalf("finish action = %T, want workflow.PromptAction", finish.Action)
	}
}

func TestParsedWorkflowDefinitionOwnsLoopBodyNodes(t *testing.T) {
	src := []byte(`
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: [redis]
      as: backend
      tasks:
        - id: handle
          depends_on: [seed]
          prompt: "probe {{backend}}"
`)
	wf, err := workflow.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	def := wf.Definition()
	loop := findLoopNode(t, def, "probe")
	if len(loop.Body.Nodes) != 1 {
		t.Fatalf("loop body nodes = %d, want 1", len(loop.Body.Nodes))
	}
	member := loop.Body.Nodes[0]
	if member.ID != "handle" {
		t.Fatalf("loop body node ID = %q, want handle", member.ID)
	}
	action, ok := member.Action.(workflow.PromptAction)
	if !ok {
		t.Fatalf("loop body action = %T, want workflow.PromptAction", member.Action)
	}
	if got := action.Prompt.String(); got != "probe {{backend}}" {
		t.Fatalf("loop body prompt = %q, want probe {{backend}}", got)
	}

	// Definition returns a copy of the parse-built semantic model: mutating the
	// caller's copy or the legacy materialized Task fields must not rewrite the
	// authoritative body node used by planning.
	loop.Body.Nodes[0].ID = "mutated"
	wf.ByID("handle").Prompt = "mutated"

	def = wf.Definition()
	loop = findLoopNode(t, def, "probe")
	member = loop.Body.Nodes[0]
	if member.ID != "handle" {
		t.Fatalf("second loop body node ID = %q, want handle", member.ID)
	}
	action, ok = member.Action.(workflow.PromptAction)
	if !ok {
		t.Fatalf("second loop body action = %T, want workflow.PromptAction", member.Action)
	}
	if got := action.Prompt.String(); got != "probe {{backend}}" {
		t.Fatalf("second loop body prompt = %q, want probe {{backend}}", got)
	}
}

func findLoopNode(t *testing.T, def workflow.WorkflowDefinition, id workflow.LoopID) workflow.LoopNode {
	t.Helper()
	for _, node := range def.Nodes {
		loop, ok := node.(workflow.LoopNode)
		if ok && loop.ID == id {
			return loop
		}
	}
	t.Fatalf("loop node %q not found", id)
	return workflow.LoopNode{}
}
