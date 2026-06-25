package workflow_test

import (
	"errors"
	"fmt"
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
			_, err := workflow.Parse([]byte(src))
			if !errors.Is(err, workflow.ErrSubWorkflowWithRuntime) {
				t.Fatalf("Parse error = %v, want ErrSubWorkflowWithRuntime for %s on a sub-workflow task", err, tc.name)
			}
		})
	}
}

// TestParseSubWorkflowAllowsTaskFields pins, one field per case, that the
// task-only fields a sub-workflow task legitimately carries are accepted and
// recorded: depends_on, when, writes_state, retry, budget, schema, and with.
func TestParseSubWorkflowAllowsTaskFields(t *testing.T) {
	cases := []struct {
		name  string
		field string // YAML lines appended to the `cut` sub-workflow task
		check func(t *testing.T, cut *workflow.Task)
	}{
		{
			name:  "DependsOn",
			field: "    depends_on: [seed]",
			check: func(t *testing.T, cut *workflow.Task) {
				if !slices.Contains(cut.DependsOn, workflow.TaskID("seed")) {
					t.Errorf("DependsOn = %v, want it to include seed", cut.DependsOn)
				}
			},
		},
		{
			name:  "When",
			field: "    depends_on: [seed]\n    when: succeeded(seed)",
			check: func(t *testing.T, cut *workflow.Task) {
				if cut.When != "succeeded(seed)" {
					t.Errorf("When = %q, want %q", cut.When, "succeeded(seed)")
				}
			},
		},
		{
			name:  "WritesState",
			field: "    writes_state: cut_out",
			check: func(t *testing.T, cut *workflow.Task) {
				if cut.WritesState != "cut_out" {
					t.Errorf("WritesState = %q, want %q", cut.WritesState, "cut_out")
				}
			},
		},
		{
			name:  "Retry.Max",
			field: "    retry:\n      max: 1",
			check: func(t *testing.T, cut *workflow.Task) {
				if cut.Retry.Max != 1 {
					t.Errorf("Retry.Max = %d, want 1", cut.Retry.Max)
				}
			},
		},
		{
			name:  "Budget",
			field: "    budget:\n      max_cost_usd: 1.0",
			check: func(t *testing.T, cut *workflow.Task) {
				if cut.Budget == nil || cut.Budget.MaxCostUSD != 1.0 {
					t.Errorf("Budget = %+v, want MaxCostUSD 1.0", cut.Budget)
				}
			},
		},
		{
			name:  "Schema",
			field: "    schema:\n      type: object",
			check: func(t *testing.T, cut *workflow.Task) {
				if cut.Schema == nil {
					t.Error("Schema = nil, want a parsed object schema")
				}
			},
		},
		{
			name:  "With",
			field: "    with:\n      version: \"{{seed}}\"",
			check: func(t *testing.T, cut *workflow.Task) {
				want := []workflow.WithArg{{Name: "version", Value: "{{seed}}"}}
				if !slices.Equal(cut.With, want) {
					t.Errorf("With = %+v, want %+v", cut.With, want)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf(`
name: parent
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: hi
  - id: cut
    workflow: release
%s
`, tc.field)
			wf, err := workflow.Parse([]byte(src))
			if err != nil {
				t.Fatalf("Parse rejected allowed sub-workflow field %s: %v", tc.name, err)
			}
			cut := wf.ByID("cut")
			if cut == nil {
				t.Fatal("ByID(cut) = nil")
			}
			tc.check(t, cut)
		})
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
	_, err := workflow.Parse([]byte(src))
	var bad *workflow.InvalidParamNameError
	if !errors.As(err, &bad) {
		t.Fatalf("errors.As InvalidParamNameError failed; err = %v", err)
	}
	if bad.Value != "bad-key" {
		t.Errorf("InvalidParamNameError.Value = %q, want %q", bad.Value, "bad-key")
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
	_, err := workflow.Parse([]byte(src))
	var unknown *workflow.UnknownOutputTaskError
	if !errors.As(err, &unknown) {
		t.Fatalf("errors.As UnknownOutputTaskError failed; err = %v", err)
	}
	if unknown.Task != "ghost" {
		t.Errorf("UnknownOutputTaskError.Task = %q, want %q", unknown.Task, "ghost")
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

// TestParseSubWorkflowWithForEachVar pins that a sub-workflow body task inside a
// for_each may reference the loop's `as` variable in a with-value, exactly as a
// member prompt may. The loop var is bound per iteration, so it is exempt from
// the depends_on check and creates no dep edge (mirroring buildDeps for prompt
// members).
func TestParseSubWorkflowWithForEachVar(t *testing.T) {
	src := `
name: parent
runtime: test-rt
model: m1
tasks:
  - id: list
    command: "echo a"
  - id: fan
    for_each:
      in: "{{list}}"
      as: item
      tasks:
        - id: run
          workflow: release
          with:
            target: "{{item}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	run := wf.ByID("run")
	if run == nil {
		t.Fatal("ByID(run) = nil")
	}
	if slices.Contains(run.DependsOn, workflow.TaskID("item")) {
		t.Errorf("loop var leaked into DependsOn = %v; want no `item` edge", run.DependsOn)
	}
}

// TestParseSubWorkflowWithPrevReferencingNonMember pins that a `{{prev.id}}`
// placeholder living in a sub-workflow task's `with:` value is validated for
// loop membership exactly as a prompt body is. The member references a task
// that is not part of its own loop, so Parse must reject it with
// PrevNotMemberError rather than letting the dangling reference through to
// resolve to "" at runtime.
//
// Regression for .loom/report.md top priority #2: checkPrevPlaceholders now
// scans the With values as well as Prompt/Command, so this reference is
// rejected at parse time.
func TestParseSubWorkflowWithPrevReferencingNonMember(t *testing.T) {
	src := `
name: wf_prev_sub_nonmember
runtime: test-rt
model: m1
tasks:
  - id: outside
    prompt: O
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks:
        - id: a
          workflow: release
          with:
            version: "{{prev.outside}}"
`
	_, err := workflow.Parse([]byte(src))

	var got *workflow.PrevNotMemberError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As PrevNotMemberError failed; err = %v", err)
	}
	if got.Task != "a" {
		t.Errorf("PrevNotMemberError.Task = %q, want a", got.Task)
	}
	if got.Name != "outside" {
		t.Errorf("PrevNotMemberError.Name = %q, want outside", got.Name)
	}
}

// TestParseSubWorkflowWithPrevOutsideLoop pins that a `{{prev.id}}` placeholder
// in a top-level sub-workflow task's `with:` value is rejected with
// PrevOutsideLoopError: prev names the prior iteration's output of a sibling
// loop member, so it is meaningless on a task that belongs to no loop.
//
// Regression for .loom/report.md top priority #2: the prev refs returned by
// scanPlaceholders for with-values are now validated (buildSubWorkflowDeps no
// longer discards them), so this reference is rejected rather than substituting
// "" at runtime.
func TestParseSubWorkflowWithPrevOutsideLoop(t *testing.T) {
	src := `
name: wf_prev_sub_toplevel
runtime: test-rt
model: m1
tasks:
  - id: cut
    workflow: release
    with:
      version: "{{prev.a}}"
`
	_, err := workflow.Parse([]byte(src))

	var got *workflow.PrevOutsideLoopError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As PrevOutsideLoopError failed; err = %v", err)
	}
	if got.Task != "cut" {
		t.Errorf("PrevOutsideLoopError.Task = %q, want cut", got.Task)
	}
	if got.Name != "a" {
		t.Errorf("PrevOutsideLoopError.Name = %q, want a", got.Name)
	}
}
