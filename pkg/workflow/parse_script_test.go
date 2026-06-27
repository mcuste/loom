package workflow_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParseScriptBodyForm pins that `script:` is accepted as a body form, that
// `args:` decodes into the typed slice, and that BodyKind reports BodyScript.
func TestParseScriptBodyForm(t *testing.T) {
	src := `
name: wf
tasks:
  - id: check
    script: ./scripts/healthcheck.sh
    args: ["prod", "--verbose"]
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	got := wf.Tasks[0]
	if !got.IsScript() {
		t.Errorf("IsScript() = false, want true")
	}
	if got.BodyKind() != workflow.BodyScript {
		t.Errorf("BodyKind() = %v, want BodyScript", got.BodyKind())
	}
	if got.Script != "./scripts/healthcheck.sh" {
		t.Errorf("Script = %q, want %q", got.Script, "./scripts/healthcheck.sh")
	}
	if !slices.Equal(got.Args, []string{"prod", "--verbose"}) {
		t.Errorf("Args = %v, want [prod --verbose]", got.Args)
	}
}

// TestParseScriptBodyConflicts pins that script is mutually exclusive with every
// other body form.
func TestParseScriptBodyConflicts(t *testing.T) {
	conflicts := map[string]string{
		"prompt": `
name: wf
runtime: rt
model: m1
tasks:
  - id: a
    script: ./x.sh
    prompt: hello
`,
		"command": `
name: wf
tasks:
  - id: a
    script: ./x.sh
    command: echo hi
`,
		"workflow": `
name: wf
tasks:
  - id: a
    script: ./x.sh
    workflow: child
`,
	}
	for other, src := range conflicts {
		t.Run(other, func(t *testing.T) {
			_, err := workflow.Parse([]byte(src))
			var conflict *workflow.TaskBodyConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("error = %v, want *TaskBodyConflictError", err)
			}
			if !slices.Contains(conflict.Fields, "script") || !slices.Contains(conflict.Fields, other) {
				t.Errorf("Fields = %v, want both script and %q", conflict.Fields, other)
			}
		})
	}
}

// TestParseScriptRejectsLLMFields pins that a script task may not set runtime,
// model, effort, system_prompt, or schema, mirroring a shell task.
func TestParseScriptRejectsLLMFields(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want error
	}{
		{"runtime", "name: wf\ntasks:\n  - id: a\n    script: ./x.sh\n    runtime: rt\n", workflow.ErrScriptTaskWithRuntime},
		{"model", "name: wf\ntasks:\n  - id: a\n    script: ./x.sh\n    model: m1\n", workflow.ErrScriptTaskWithRuntime},
		{"effort", "name: wf\ntasks:\n  - id: a\n    script: ./x.sh\n    effort: low\n", workflow.ErrScriptTaskWithRuntime},
		{"system_prompt", "name: wf\ntasks:\n  - id: a\n    script: ./x.sh\n    system_prompt: be brief\n", workflow.ErrScriptTaskWithSystemPrompt},
		{"schema", "name: wf\ntasks:\n  - id: a\n    script: ./x.sh\n    schema:\n      type: object\n", workflow.ErrScriptTaskWithSchema},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workflow.Parse([]byte(tc.src))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestParseArgsWithoutScript pins that `args:` is rejected on a non-script task.
func TestParseArgsWithoutScript(t *testing.T) {
	src := `
name: wf
runtime: rt
model: m1
tasks:
  - id: a
    prompt: hello
    args: ["x"]
`
	_, err := workflow.Parse([]byte(src))
	if !errors.Is(err, workflow.ErrArgsWithoutScript) {
		t.Fatalf("error = %v, want ErrArgsWithoutScript", err)
	}
}

// TestParseScriptPlaceholderDeps pins that `{{id}}` and `{{id.exit}}` references
// in the script path and args create dependency edges and are validated against
// depends_on.
func TestParseScriptPlaceholderDeps(t *testing.T) {
	src := `
name: wf
tasks:
  - id: probe
    command: echo ./bin
  - id: run
    depends_on: [probe]
    script: "{{probe}}/tool.sh"
    args: ["code-{{probe.exit}}"]
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	var run workflow.Task
	for _, tk := range wf.Tasks {
		if tk.ID == "run" {
			run = tk
		}
	}
	if !slices.Contains(run.DependsOn, workflow.TaskID("probe")) {
		t.Errorf("DependsOn = %v, want it to contain probe", run.DependsOn)
	}
}

// TestParseScriptExitRefMissingDep pins that an `{{id.exit}}` reference whose
// producer is absent from depends_on is rejected, exactly like a bare `{{id}}`.
func TestParseScriptExitRefMissingDep(t *testing.T) {
	src := `
name: wf
tasks:
  - id: probe
    command: echo hi
  - id: run
    script: ./tool.sh
    args: ["code-{{probe.exit}}"]
`
	if _, err := workflow.Parse([]byte(src)); err == nil {
		t.Fatal("Parse returned nil error, want an unknown-placeholder failure for the undeclared dependency")
	}
}
