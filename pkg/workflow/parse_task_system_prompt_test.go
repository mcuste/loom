package workflow_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParseTaskSystemPromptOverride pins that a task-level system_prompt parses
// and lands in Task.SystemPrompt while the workflow-level default is preserved
// for tasks that do not override it.
func TestParseTaskSystemPromptOverride(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
system_prompt: workflow default
tasks:
  - id: a
    system_prompt: task override
    prompt: go
  - id: b
    depends_on: [a]
    prompt: "see {{a}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := wf.ByID("a").SystemPrompt; got != "task override" {
		t.Errorf("task a SystemPrompt = %q, want %q", got, "task override")
	}
	if got := wf.ByID("b").SystemPrompt; got != "" {
		t.Errorf("task b SystemPrompt = %q, want empty (inherits workflow default)", got)
	}
	if got := wf.EffectiveSystemPrompt(wf.ByID("b")); got != "workflow default" {
		t.Errorf("EffectiveSystemPrompt(b) = %q, want %q", got, "workflow default")
	}
}

// TestParseTaskSystemPromptShellRejected pins that a shell task may not carry a
// task-level system_prompt: a system prompt is meaningless for a command task.
func TestParseTaskSystemPromptShellRejected(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    command: "echo hi"
    system_prompt: nope
`
	_, err := workflow.Parse([]byte(src))
	if !errors.Is(err, workflow.ErrShellTaskWithSystemPrompt) {
		t.Fatalf("errors.Is(_, ErrShellTaskWithSystemPrompt) = false; err = %v", err)
	}
}

// TestParseTaskSystemPromptSubWorkflowRejected pins that a sub-workflow task may
// not carry a task-level system_prompt: the linked child brings its own.
func TestParseTaskSystemPromptSubWorkflowRejected(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    workflow: ./child.yaml
    system_prompt: nope
`
	_, err := workflow.Parse([]byte(src))
	if !errors.Is(err, workflow.ErrSubWorkflowWithSystemPrompt) {
		t.Fatalf("errors.Is(_, ErrSubWorkflowWithSystemPrompt) = false; err = %v", err)
	}
}

// TestParseTaskSystemPromptLoopWrapperRejected pins that a loop-wrapper task may
// not carry a system_prompt; the field belongs to the body tasks.
func TestParseTaskSystemPromptLoopWrapperRejected(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: work
    system_prompt: nope
    loop:
      until_empty: drain
      max: 3
      tasks:
        - id: drain
          prompt: drain
`
	_, err := workflow.Parse([]byte(src))
	if !errors.Is(err, workflow.ErrLoopTaskWithFields) {
		t.Fatalf("errors.Is(_, ErrLoopTaskWithFields) = false; err = %v", err)
	}
}

// TestParseTaskSystemPromptTaskRefRejected pins that a task-level system_prompt,
// like the workflow-level one, may not reference task-id placeholders (they are
// never resolvable in a system prompt).
func TestParseTaskSystemPromptTaskRefRejected(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: first
  - id: b
    depends_on: [a]
    system_prompt: "context {{a}}"
    prompt: go
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.SystemPlaceholderTaskRefError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As SystemPlaceholderTaskRefError failed; err = %v", err)
	}
}

// TestParseTaskSystemPromptUnknownParamRejected pins that a {{params.x}} in a
// task system_prompt must name a declared param.
func TestParseTaskSystemPromptUnknownParamRejected(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    system_prompt: "ctx={{params.missing}}"
    prompt: go
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownParamError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownParamError failed; err = %v", err)
	}
}

// TestParseTaskSystemPromptCountsParamUsed pins that a param referenced only in
// a task-level system_prompt is counted as used (no UnusedParamError).
func TestParseTaskSystemPromptCountsParamUsed(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
params:
  - name: persona
tasks:
  - id: a
    system_prompt: "you are {{params.persona}}"
    prompt: go
`
	if _, err := workflow.Parse([]byte(src)); err != nil {
		t.Fatalf("Parse rejected param used only in task system_prompt: %v", err)
	}
}

// TestParseTaskSystemPromptFileNotInlinedRejected pins that a task-level
// system_prompt_file reaching Parse uninlined is rejected (mirroring prompt_file)
// rather than silently dropped, since rawTask.SystemPromptFile never becomes a
// Task field.
func TestParseTaskSystemPromptFileNotInlinedRejected(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: go
    system_prompt_file: sys.md
`
	_, err := workflow.Parse([]byte(src))
	if err == nil {
		t.Fatal("Parse accepted an uninlined task system_prompt_file; want rejection")
	}
	if !strings.Contains(err.Error(), "must be inlined") {
		t.Errorf("error %q does not mention inlining requirement", err)
	}
}
