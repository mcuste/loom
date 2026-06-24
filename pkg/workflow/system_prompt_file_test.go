package workflow_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestInlineSystemPromptFileRelative pins that a relative system_prompt_file
// resolves against baseDir, the returned bytes carry `system_prompt:` and never
// `system_prompt_file:`, and the inlined system prompt equals the file content
// after a round-trip Parse.
func TestInlineSystemPromptFileRelative(t *testing.T) {
	dir := t.TempDir()
	const body = "you are a careful reviewer\napply the catalog\n"
	writeFile(t, dir, "prompts/system.md", body)
	wf := `
name: wf
runtime: test-rt
model: m1
system_prompt_file: prompts/system.md
tasks:
  - id: a
    prompt: go
`
	out, err := workflow.InlinePromptFiles([]byte(wf), dir)
	if err != nil {
		t.Fatalf("InlinePromptFiles: %v", err)
	}
	if strings.Contains(string(out), "system_prompt_file") {
		t.Errorf("output still contains system_prompt_file:\n%s", out)
	}
	if !strings.Contains(string(out), "system_prompt:") {
		t.Errorf("output missing system_prompt: key:\n%s", out)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(inlined): %v", err)
	}
	if got := parsed.SystemPrompt; got != body {
		t.Errorf("SystemPrompt = %q, want %q", got, body)
	}
}

// TestInlineSystemPromptFileAbsoluteRejected pins that an absolute
// system_prompt_file path is rejected with AbsoluteSystemPromptFilePathError.
func TestInlineSystemPromptFileAbsoluteRejected(t *testing.T) {
	dir := t.TempDir()
	abs := writeFile(t, dir, "system.md", "x\n")
	wf := `
name: wf
runtime: test-rt
model: m1
system_prompt_file: ` + abs + `
tasks:
  - id: a
    prompt: go
`
	_, err := workflow.InlinePromptFiles([]byte(wf), dir)
	var got *workflow.AbsoluteSystemPromptFilePathError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As AbsoluteSystemPromptFilePathError failed; err = %v", err)
	}
	if got.Path == "" {
		t.Errorf("AbsoluteSystemPromptFilePathError.Path is empty")
	}
}

// TestInlineSystemPromptFileMissing pins that an unreadable system_prompt_file
// yields a SystemPromptFileError carrying the path and wrapping os.ErrNotExist.
func TestInlineSystemPromptFileMissing(t *testing.T) {
	dir := t.TempDir()
	wf := `
name: wf
runtime: test-rt
model: m1
system_prompt_file: prompts/missing.md
tasks:
  - id: a
    prompt: go
`
	_, err := workflow.InlinePromptFiles([]byte(wf), dir)
	var got *workflow.SystemPromptFileError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As SystemPromptFileError failed; err = %v", err)
	}
	if got.Path == "" {
		t.Errorf("SystemPromptFileError.Path is empty")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("errors.Is(_, os.ErrNotExist) = false; err = %v", err)
	}
}

// TestInlineSystemPromptFileConflict pins that setting both the inline
// system_prompt and system_prompt_file is rejected with ErrSystemPromptAndFileSet.
func TestInlineSystemPromptFileConflict(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "system.md", "from file\n")
	wf := `
name: wf
runtime: test-rt
model: m1
system_prompt: inline one
system_prompt_file: system.md
tasks:
  - id: a
    prompt: go
`
	_, err := workflow.InlinePromptFiles([]byte(wf), dir)
	if !errors.Is(err, workflow.ErrSystemPromptAndFileSet) {
		t.Fatalf("errors.Is(_, ErrSystemPromptAndFileSet) = false; err = %v", err)
	}
}

// TestParseFileSystemPromptFile pins the end-to-end ParseFile path: the file is
// read relative to the YAML's own directory and lands in Workflow.SystemPrompt.
func TestParseFileSystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	const body = "shared reviewer context\n"
	writeFile(t, dir, "prompts/system.md", body)
	path := writeFile(t, dir, "wf.yaml", `
name: wf
runtime: test-rt
model: m1
system_prompt_file: prompts/system.md
tasks:
  - id: a
    prompt: go
`)
	wf, err := workflow.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got := wf.SystemPrompt; got != body {
		t.Errorf("SystemPrompt = %q, want %q", got, body)
	}
}

// TestSystemPromptFileOnTaskRejected pins that system_prompt_file is a
// workflow-level key only: nested in a task it is left untouched by inlining and
// rejected by Parse's known-fields decoder.
func TestSystemPromptFileOnTaskRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "system.md", "x\n")
	wf := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: go
    system_prompt_file: system.md
`
	out, err := workflow.InlinePromptFiles([]byte(wf), dir)
	if err != nil {
		t.Fatalf("InlinePromptFiles: %v", err)
	}
	if !strings.Contains(string(out), "system_prompt_file") {
		t.Errorf("task-level system_prompt_file should be left for Parse to reject:\n%s", out)
	}
	if _, err := workflow.Parse(out); err == nil {
		t.Fatalf("Parse accepted task-level system_prompt_file; want unknown-field error")
	}
}
