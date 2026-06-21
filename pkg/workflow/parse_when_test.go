package workflow_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParseWhen_AttachesExprToTask pins that a valid `when:` expression
// round-trips onto the task verbatim.
func TestParseWhen_AttachesExprToTask(t *testing.T) {
	src := `
name: wf_when
runtime: test-rt
model: m1
tasks:
  - id: gate
    prompt: decide
  - id: guarded
    depends_on: [gate]
    when: '{{gate}} == "go"'
    prompt: run it
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.Tasks[1].When != `{{gate}} == "go"` {
		t.Errorf("task guarded When = %q, want %q", wf.Tasks[1].When, `{{gate}} == "go"`)
	}
}

// TestParseWhen_RejectsUnknownRef pins that a `when:` referencing a task id not
// in the workflow fails to load with UnknownConditionRefError.
func TestParseWhen_RejectsUnknownRef(t *testing.T) {
	src := `
name: wf_when
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
    when: succeeded(ghost)
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownConditionRefError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownConditionRefError failed; err = %v", err)
	}
}

// TestParseWhen_RejectsRefNotInDependsOn pins that a `when:` placeholder naming
// a real task absent from the guarded task's depends_on is rejected at load
// time: the executor waits only on dependency gates, so such a reference could
// read an output before it is written.
func TestParseWhen_RejectsRefNotInDependsOn(t *testing.T) {
	src := `
name: wf_when
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
  - id: b
    prompt: B
    when: '{{a}} == "yes"'
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownConditionRefError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownConditionRefError failed; err = %v", err)
	}
}

// TestParseWhen_RejectsSelfRef pins that a `when:` referencing the guarded
// task's own id is rejected: the task is not its own dependency, so the
// reference can never resolve.
func TestParseWhen_RejectsSelfRef(t *testing.T) {
	src := `
name: wf_when
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
    when: succeeded(a)
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownConditionRefError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownConditionRefError failed; err = %v", err)
	}
}

// TestParseWhen_RejectsMalformedExpr pins that a malformed `when:` expression
// fails to load with MalformedConditionError.
func TestParseWhen_RejectsMalformedExpr(t *testing.T) {
	src := `
name: wf_when
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
  - id: b
    depends_on: [a]
    prompt: B
    when: '{{a}} === 1'
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.MalformedConditionError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As MalformedConditionError failed; err = %v", err)
	}
}
