package workflow_test

import (
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestPlanDeclarationTieBreak pins the tie-break rule: when multiple tasks are
// ready in the same Kahn step, declaration order wins. The fixture has three
// independent roots (joke, fact, tip) and a downstream task (summary); after
// the roots run, summary must come last.
func TestPlanDeclarationTieBreak(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: joke
    prompt: x
  - id: fact
    prompt: x
  - id: tip
    prompt: x
  - id: summary
    depends_on: [joke, fact, tip]
    prompt: |
      {{joke}} {{fact}} {{tip}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.Plan()
	want := []workflow.TaskID{"joke", "fact", "tip", "summary"}
	if !slices.Equal(got, want) {
		t.Fatalf("Plan = %v, want %v", got, want)
	}
}

// TestPlanRespectsDependencies pins that a dependent never appears before any
// dependency, even when declared above it in the YAML.
func TestPlanRespectsDependencies(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: c
    depends_on: [a, b]
    prompt: use {{a}} and {{b}}
  - id: b
    depends_on: [a]
    prompt: use {{a}}
  - id: a
    prompt: x
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.Plan()
	want := []workflow.TaskID{"a", "b", "c"}
	if !slices.Equal(got, want) {
		t.Fatalf("Plan = %v, want %v", got, want)
	}
}

func TestPlanSingleTask(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: only
    prompt: x
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.Plan()
	if !slices.Equal(got, []workflow.TaskID{"only"}) {
		t.Fatalf("Plan = %v, want [only]", got)
	}
}

func TestByID(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: pa
  - id: b
    prompt: pb
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.ByID("b")
	if got == nil || got.Prompt != "pb" {
		t.Fatalf("ByID(b) = %+v, want task with prompt pb", got)
	}
	if wf.ByID("missing") != nil {
		t.Fatalf("ByID(missing) = non-nil, want nil")
	}
}
