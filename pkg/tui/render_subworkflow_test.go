package tui_test

import (
	"bytes"
	"strings"
	"testing"

	_ "github.com/mcuste/loom/pkg/runtime/claudecode" // register the claude-code runtime
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// subParentFixture links a child workflow from the `cut` task. The `{{pick}}`
// placeholder in the with-value supplies cut's dependency on pick; announce
// consumes cut's result. Parse records cut.Workflow but does NOT populate Subs
// (the CLI link step does), so tests set it by hand.
const subParentFixture = `
name: parent
tasks:
  - id: pick
    command: "printf 1.4.0"
  - id: cut
    workflow: ./release.yaml
    with:
      version: "{{pick}}"
  - id: announce
    depends_on: [cut]
    command: "printf 'done %s' '{{cut}}'"
`

const subChildFixture = `
name: release
output: publish
params:
  - name: version
    required: true
tasks:
  - id: build
    command: "printf 'built v%s' '{{params.version}}'"
  - id: publish
    depends_on: [build]
    command: "printf '%s -> published' '{{build}}'"
`

// linkedParent parses the parent and child fixtures and wires the child into
// the parent's Subs under the `cut` task, mirroring what the CLI link step does
// before rendering.
func linkedParent(t *testing.T) *workflow.Workflow {
	t.Helper()
	parent, err := workflow.Parse([]byte(subParentFixture))
	if err != nil {
		t.Fatalf("parse parent: %v", err)
	}
	child, err := workflow.Parse([]byte(subChildFixture))
	if err != nil {
		t.Fatalf("parse child: %v", err)
	}
	parent.Subs = map[workflow.TaskID]*workflow.Workflow{"cut": child}
	return parent
}

// TestRenderPlan_SubworkflowRow pins that a sub-workflow task renders with the
// subworkflow kind, its linked ref, the resolved child-task count, and the
// child's tasks listed indented beneath it, in both the plain and rich plans.
func TestRenderPlan_SubworkflowRow(t *testing.T) {
	forceASCIIProfile(t)
	wf := linkedParent(t)
	resolved := workflow.ParamValues{}

	for _, rich := range []bool{false, true} {
		got := tui.RenderPlan(wf, resolved, nil, rich)
		for _, want := range []string{
			"kind=subworkflow",
			"workflow=./release.yaml",
			"(2 tasks)",
			"build",
			"publish",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("RenderPlan(rich=%v) missing %q; got:\n%s", rich, want, got)
			}
		}
		// The redundant LLM columns must not leak onto a sub-workflow row.
		for _, line := range strings.Split(got, "\n") {
			if strings.Contains(line, "cut ") && strings.Contains(line, "runtime=") {
				t.Errorf("RenderPlan(rich=%v) rendered sub-workflow row as an LLM row: %q", rich, line)
			}
		}
	}
}

// TestRenderPlan_SubworkflowUnlinked pins that without a resolved child (Subs
// nil) the row still renders the ref, just without the child-task count or body
// rows, so plan printing never panics on an unlinked manifest.
func TestRenderPlan_SubworkflowUnlinked(t *testing.T) {
	forceASCIIProfile(t)
	wf, err := workflow.Parse([]byte(subParentFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := tui.RenderPlan(wf, workflow.ParamValues{}, nil, true)
	if !strings.Contains(got, "workflow=./release.yaml") {
		t.Errorf("unlinked sub-workflow row missing ref; got:\n%s", got)
	}
	if strings.Contains(got, "tasks)") {
		t.Errorf("unlinked sub-workflow row should not claim a child-task count; got:\n%s", got)
	}
}

// TestShowRun_SubworkflowRouting pins that the inline `runs show` summary labels
// a sub-workflow task by its linked ref (from the embedded manifest) rather than
// the empty model dash a body-less record would otherwise produce.
func TestShowRun_SubworkflowRouting(t *testing.T) {
	rec := &store.RunRecord{
		RunID:      "20260101T000000Z-abcdef",
		WorkflowID: "parent",
		Status:     store.StatusOK,
		Manifest:   subParentFixture,
		Tasks: []store.TaskRecord{
			{ID: "cut", Status: store.StatusOK, Output: "built v1.4.0 -> published"},
		},
	}
	var buf bytes.Buffer
	if err := tui.ShowRun(&buf, rec, false); err != nil {
		t.Fatalf("ShowRun: %v", err)
	}
	if !strings.Contains(buf.String(), "(subworkflow ./release.yaml)") {
		t.Errorf("runs show should label the sub-workflow task; got:\n%s", buf.String())
	}
}
