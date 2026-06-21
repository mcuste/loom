package workflow_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParseWritesState pins that a valid `writes_state` key is parsed onto the
// task for both LLM and shell tasks.
func TestParseWritesState(t *testing.T) {
	src := `
name: wf_state
runtime: test-rt
model: m1
tasks:
  - id: discover
    prompt: list work
    writes_state: backlog
  - id: record
    command: echo done
    writes_state: cursor
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.Tasks[0].WritesState != "backlog" {
		t.Errorf("task discover WritesState = %q, want backlog", wf.Tasks[0].WritesState)
	}
	if wf.Tasks[1].WritesState != "cursor" {
		t.Errorf("task record WritesState = %q, want cursor", wf.Tasks[1].WritesState)
	}
}

// TestParseInvalidWritesState pins that a `writes_state` value outside the
// identifier alphabet is rejected with InvalidWritesStateError.
func TestParseInvalidWritesState(t *testing.T) {
	src := `
name: wf_state
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
    writes_state: "bad key"
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.InvalidWritesStateError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As InvalidWritesStateError failed; err = %v", err)
	}
	if got.Task != "a" || got.Key != "bad key" {
		t.Errorf("InvalidWritesStateError = %+v, want task=a key=%q", got, "bad key")
	}
}

// TestStatePlaceholderNeedsNoDeclaration pins that a `{{state.key}}`
// placeholder neither requires a depends_on entry nor is flagged as an unknown
// task/param reference, and that it creates no dependency edge.
func TestStatePlaceholderNeedsNoDeclaration(t *testing.T) {
	src := `
name: wf_state
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: |
      skip {{state.done}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(wf.Tasks[0].DependsOn) != 0 {
		t.Errorf("task a DependsOn = %v, want none (state refs add no edges)", wf.Tasks[0].DependsOn)
	}
}

// TestStatePlaceholderInSystemPromptAllowed pins that `{{state.key}}` is
// tolerated in the workflow-level system_prompt (it resolves at run time),
// unlike a bare task reference which is rejected there.
func TestStatePlaceholderInSystemPromptAllowed(t *testing.T) {
	src := `
name: wf_state
runtime: test-rt
model: m1
system_prompt: remember {{state.note}}
tasks:
  - id: a
    prompt: A
`
	if _, err := workflow.Parse([]byte(src)); err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
}
