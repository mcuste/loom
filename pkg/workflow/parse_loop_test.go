package workflow_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParseLoop pins that a well-formed `loop:` block is parsed onto the
// workflow with its until_empty task and max iteration cap.
func TestParseLoop(t *testing.T) {
	src := `
name: wf_loop
runtime: test-rt
model: m1
loop:
  until_empty: discover
  max: 10
tasks:
  - id: discover
    prompt: list work {{state.done}}
    writes_state: done
  - id: handle
    depends_on: [discover]
    prompt: |
      handle {{discover}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.Loop == nil {
		t.Fatalf("wf.Loop = nil, want non-nil")
	}
	if wf.Loop.UntilEmpty != "discover" {
		t.Errorf("Loop.UntilEmpty = %q, want discover", wf.Loop.UntilEmpty)
	}
	if wf.Loop.Max != 10 {
		t.Errorf("Loop.Max = %d, want 10", wf.Loop.Max)
	}
}

// TestParseNoLoop pins that a workflow with no `loop:` key parses to a nil
// Loop (the run-once default).
func TestParseNoLoop(t *testing.T) {
	src := `
name: wf_once
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
	if wf.Loop != nil {
		t.Errorf("wf.Loop = %+v, want nil", wf.Loop)
	}
}

// TestParseLoopUnknownTask pins that until_empty naming a task absent from the
// workflow is rejected with UnknownLoopTaskError.
func TestParseLoopUnknownTask(t *testing.T) {
	src := `
name: wf_loop
runtime: test-rt
model: m1
loop:
  until_empty: ghost
  max: 3
tasks:
  - id: a
    prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownLoopTaskError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownLoopTaskError failed; err = %v", err)
	}
	if got.Task != "ghost" {
		t.Errorf("UnknownLoopTaskError.Task = %q, want ghost", got.Task)
	}
}

// TestParseLoopMaxBelowOne pins that max < 1 is rejected with
// InvalidLoopMaxError. max: 0 (and a missing max, which defaults to 0) leave
// the loop unable to run a single pass.
func TestParseLoopMaxBelowOne(t *testing.T) {
	for _, max := range []string{"0", "-1"} {
		src := `
name: wf_loop
runtime: test-rt
model: m1
loop:
  until_empty: a
  max: ` + max + `
tasks:
  - id: a
    prompt: A
`
		_, err := workflow.Parse([]byte(src))
		var got *workflow.InvalidLoopMaxError
		if !errors.As(err, &got) {
			t.Fatalf("max=%s: errors.As InvalidLoopMaxError failed; err = %v", max, err)
		}
	}
}

// TestParseLoopMissingUntilEmpty pins that a `loop:` block without
// `until_empty` is rejected with ErrLoopMissingUntilEmpty.
func TestParseLoopMissingUntilEmpty(t *testing.T) {
	src := `
name: wf_loop
runtime: test-rt
model: m1
loop:
  max: 3
tasks:
  - id: a
    prompt: A
`
	_, err := workflow.Parse([]byte(src))
	if !errors.Is(err, workflow.ErrLoopMissingUntilEmpty) {
		t.Fatalf("errors.Is ErrLoopMissingUntilEmpty failed; err = %v", err)
	}
}

// TestParseLoopUnknownField pins that an unrecognized key inside `loop:` is
// rejected with UnknownLoopFieldError rather than silently ignored.
func TestParseLoopUnknownField(t *testing.T) {
	src := `
name: wf_loop
runtime: test-rt
model: m1
loop:
  until_empty: a
  max: 3
  forever: true
tasks:
  - id: a
    prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownLoopFieldError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownLoopFieldError failed; err = %v", err)
	}
	if got.Field != "forever" {
		t.Errorf("UnknownLoopFieldError.Field = %q, want forever", got.Field)
	}
}

// TestParseLoopNotMapping pins that a non-mapping `loop:` node is a structural
// error.
func TestParseLoopNotMapping(t *testing.T) {
	src := `
name: wf_loop
runtime: test-rt
model: m1
loop: [a, b]
tasks:
  - id: a
    prompt: A
`
	if _, err := workflow.Parse([]byte(src)); err == nil {
		t.Fatal("Parse accepted a sequence loop: node, want error")
	}
}
