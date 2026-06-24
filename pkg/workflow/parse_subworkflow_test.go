package workflow_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// subParent is a workflow whose single task links a child by registry name and
// passes one value through `with:`. It parses cleanly once the `workflow:` body
// form is supported; the child is resolved by the CLI link step, not by Parse.
const subParent = `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: cut_release
    workflow: release
    with:
      version: "1.4.0"
`

// TestParseSubWorkflowBodyForm pins that `workflow:` is accepted as a sixth body
// form and that `with:` decodes into ordered WithArg entries. The child is not
// resolved here (Parse stays filesystem-free), so only the raw ref and the
// with-values are asserted.
func TestParseSubWorkflowBodyForm(t *testing.T) {
	wf, err := workflow.Parse([]byte(subParent))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(wf.Tasks) != 1 {
		t.Fatalf("len(Tasks) = %d, want 1", len(wf.Tasks))
	}
	got := wf.Tasks[0]
	if !got.IsSubWorkflow() {
		t.Errorf("IsSubWorkflow() = false, want true for a workflow: task")
	}
	if got.Workflow != "release" {
		t.Errorf("Workflow = %q, want %q", got.Workflow, "release")
	}
	want := []workflow.WithArg{{Name: "version", Value: "1.4.0"}}
	if !slices.Equal(got.With, want) {
		t.Errorf("With = %+v, want %+v", got.With, want)
	}
	// Subs is populated by the CLI link step, never by Parse.
	if wf.Subs != nil {
		t.Errorf("Parse populated Subs = %+v, want nil (linking is a CLI step)", wf.Subs)
	}
}

// TestParseSubWorkflowBodyConflicts pins that `workflow:` is mutually exclusive
// with every other body form: prompt, prompt_file, command, loop, and for_each.
// Each pairing must surface a TaskBodyConflictError naming both offending keys.
func TestParseSubWorkflowBodyConflicts(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		other string
	}{
		{
			name:  "workflow and prompt",
			other: "prompt",
			body: `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: a
    workflow: release
    prompt: hi
`,
		},
		{
			name:  "workflow and command",
			other: "command",
			body: `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: a
    workflow: release
    command: echo hi
`,
		},
		{
			name:  "workflow and prompt_file",
			other: "prompt_file",
			body: `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: a
    workflow: release
    prompt_file: ./p.txt
`,
		},
		{
			name:  "workflow and loop",
			other: "loop",
			body: `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: a
    workflow: release
    loop:
      until: done
      tasks:
        - id: m
          prompt: x
`,
		},
		{
			name:  "workflow and for_each",
			other: "for_each",
			body: `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: a
    workflow: release
    for_each:
      items: ["x"]
      as: it
      tasks:
        - id: m
          prompt: "{{it}}"
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workflow.Parse([]byte(tc.body))
			var conflict *workflow.TaskBodyConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("errors.As TaskBodyConflictError failed; err = %v", err)
			}
			if !slices.Contains(conflict.Fields, "workflow") {
				t.Errorf("conflict.Fields = %v, want it to include %q", conflict.Fields, "workflow")
			}
			if !slices.Contains(conflict.Fields, tc.other) {
				t.Errorf("conflict.Fields = %v, want it to include %q", conflict.Fields, tc.other)
			}
		})
	}
}

// TestParseSubWorkflowRejectsRuntimeFields pins that runtime/model/effort are
// rejected on a sub-workflow task: it has no runtime of its own (the child
// brings its own), so these knobs are meaningless, exactly as for a shell task.
func TestParseSubWorkflowRejectsRuntimeFields(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"runtime", "    runtime: test-rt\n"},
		{"model", "    model: m1\n"},
		{"effort", "    effort: low\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "name: parent\nruntime: test-rt\nmodel: m1\ntasks:\n  - id: a\n    workflow: release\n" + tc.field
			if _, err := workflow.Parse([]byte(src)); err == nil {
				t.Fatalf("Parse accepted %s on a sub-workflow task; want rejection", tc.name)
			}
		})
	}
}

// TestParseSubWorkflowAllowsTaskFields pins that the task-only fields a
// sub-workflow task legitimately carries are accepted: depends_on, when,
// writes_state, retry, budget, schema, and with.
func TestParseSubWorkflowAllowsTaskFields(t *testing.T) {
	src := `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: hi
  - id: cut
    workflow: release
    depends_on: [seed]
    when: succeeded(seed)
    writes_state: cut_out
    retry:
      max: 1
    budget:
      max_cost_usd: 1.0
    schema:
      type: object
    with:
      version: "{{seed}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse rejected allowed sub-workflow fields: %v", err)
	}
	cut := wf.ByID("cut")
	if cut == nil {
		t.Fatal("ByID(cut) = nil")
	}
	if !slices.Contains(cut.DependsOn, workflow.TaskID("seed")) {
		t.Errorf("DependsOn = %v, want it to include seed", cut.DependsOn)
	}
}

// TestParseSubWorkflowBadWithKey pins that a `with:` key that is not a valid
// identifier is rejected at parse time.
func TestParseSubWorkflowBadWithKey(t *testing.T) {
	src := `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: a
    workflow: release
    with:
      "bad-key": "1.0"
`
	if _, err := workflow.Parse([]byte(src)); err == nil {
		t.Fatal("Parse accepted a non-identifier with: key; want rejection")
	}
}

// TestParseTopLevelOutputUnknownTask pins that a top-level `output:` naming a
// task that does not exist is rejected at parse time.
func TestParseTopLevelOutputUnknownTask(t *testing.T) {
	src := `
name: parent
runtime: test-rt
model: m1
output: ghost
tasks:
  - id: a
    prompt: hi
`
	if _, err := workflow.Parse([]byte(src)); err == nil {
		t.Fatal("Parse accepted output: naming an unknown task; want rejection")
	}
}

// TestParseTopLevelOutputKnownTask pins that a valid top-level `output:` parses
// and is recorded on the workflow.
func TestParseTopLevelOutputKnownTask(t *testing.T) {
	src := `
name: parent
runtime: test-rt
model: m1
output: a
tasks:
  - id: a
    prompt: hi
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.Output != "a" {
		t.Errorf("Output = %q, want %q", wf.Output, "a")
	}
}

// TestParseSubWorkflowWithPlaceholderDeps pins that a sub-workflow task's
// implicit dependencies come from scanning placeholders in its `with:` values
// (there is no prompt body to scan): a `{{seed}}` value must create the
// seed->cut edge without an explicit depends_on.
func TestParseSubWorkflowWithPlaceholderDeps(t *testing.T) {
	src := `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: hi
  - id: cut
    workflow: release
    with:
      version: "{{seed}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	cut := wf.ByID("cut")
	if cut == nil {
		t.Fatal("ByID(cut) = nil")
	}
	if !slices.Contains(cut.DependsOn, workflow.TaskID("seed")) {
		t.Errorf("with-value placeholder did not create a dep edge; DependsOn = %v, want [seed]", cut.DependsOn)
	}
}

// TestParseSubWorkflowWithUnknownPlaceholderDep pins the flip side: a `{{x}}`
// placeholder in a with-value that names neither a known task nor an explicit
// dependency is rejected, mirroring prompt placeholder validation.
func TestParseSubWorkflowWithUnknownPlaceholderDep(t *testing.T) {
	src := `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: cut
    workflow: release
    with:
      version: "{{ghost}}"
`
	_, err := workflow.Parse([]byte(src))
	var ue *workflow.UnknownPlaceholderError
	if !errors.As(err, &ue) {
		t.Fatalf("errors.As UnknownPlaceholderError failed; err = %v", err)
	}
}
