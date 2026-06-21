package workflow_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParse_SchemaBlockPopulatesTaskSchema pins that a well-formed per-task
// `schema:` block is parsed onto the task as a JSON Schema subset: its type,
// required list, and property types are carried onto Task.Schema.
func TestParse_SchemaBlockPopulatesTaskSchema(t *testing.T) {
	t.Parallel()
	src := `
name: wf_schema
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
    schema:
      type: object
      required: [name]
      properties:
        name:
          type: string
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	task := wf.ByID("a")
	if task == nil {
		t.Fatalf("task a not found")
	}
	if task.Schema == nil {
		t.Fatalf("task.Schema = nil, want non-nil")
	}
	if task.Schema.Type != "object" {
		t.Errorf("Schema.Type = %q, want object", task.Schema.Type)
	}
	if len(task.Schema.Required) != 1 || task.Schema.Required[0] != "name" {
		t.Errorf("Schema.Required = %v, want [name]", task.Schema.Required)
	}
	if p, ok := task.Schema.Properties["name"]; !ok || p.Type != "string" {
		t.Errorf("Schema.Properties[name] = %+v (ok=%v), want {Type:string}", p, ok)
	}
}

// TestParse_NoSchemaIsNil pins that a task without a `schema:` key parses to a
// nil Schema, the no-validation default.
func TestParse_NoSchemaIsNil(t *testing.T) {
	t.Parallel()
	src := `
name: wf_noschema
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	got := wf.ByID("a")
	if got == nil {
		t.Fatalf("task a not found")
	}
	if got.Schema != nil {
		t.Errorf("task.Schema = %+v, want nil", got.Schema)
	}
}

// TestParse_RejectsSchemaOnShellTask pins that a shell task (one with
// `command:`) that also sets a `schema:` block is rejected with
// ErrShellTaskWithSchema, since validation only applies to LLM output.
func TestParse_RejectsSchemaOnShellTask(t *testing.T) {
	t.Parallel()
	src := `
name: wf_shell_schema
runtime: test-rt
model: m1
tasks:
  - id: a
    command: "echo hi"
    schema:
      type: object
`
	_, err := workflow.Parse([]byte(src))
	if !errors.Is(err, workflow.ErrShellTaskWithSchema) {
		t.Fatalf("errors.Is ErrShellTaskWithSchema = false; err = %v", err)
	}
}
