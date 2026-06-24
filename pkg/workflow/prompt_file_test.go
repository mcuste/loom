package workflow_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// writeFile writes content to filepath.Join(dir, rel), creating parent dirs.
func writeFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", p, err)
	}
	return p
}

// TestInlinePromptFilesRelative pins that a relative prompt_file resolves
// against baseDir, the returned bytes carry `prompt:` and never `prompt_file:`,
// and the inlined prompt equals the file content after a round-trip Parse.
func TestInlinePromptFilesRelative(t *testing.T) {
	dir := t.TempDir()
	const body = "summarise the input thoroughly\nin two paragraphs\n"
	writeFile(t, dir, "prompts/a.md", body)
	wf := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt_file: prompts/a.md
`
	out, err := workflow.InlinePromptFiles([]byte(wf), dir)
	if err != nil {
		t.Fatalf("InlinePromptFiles: %v", err)
	}
	if strings.Contains(string(out), "prompt_file") {
		t.Errorf("output still contains prompt_file:\n%s", out)
	}
	if !strings.Contains(string(out), "prompt:") {
		t.Errorf("output missing prompt: key:\n%s", out)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(inlined): %v", err)
	}
	if got := parsed.Tasks[0].Prompt; got != body {
		t.Errorf("Prompt = %q, want %q", got, body)
	}
}

// TestInlinePromptFilesParentDir pins that a `../shared/x.md` path escaping the
// workflow subdir is allowed and resolves against baseDir.
func TestInlinePromptFilesParentDir(t *testing.T) {
	root := t.TempDir()
	const body = "shared system context\n"
	writeFile(t, root, "shared/ctx.md", body)
	baseDir := filepath.Join(root, "flows")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	wf := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt_file: ../shared/ctx.md
`
	out, err := workflow.InlinePromptFiles([]byte(wf), baseDir)
	if err != nil {
		t.Fatalf("InlinePromptFiles: %v", err)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(inlined): %v", err)
	}
	if got := parsed.Tasks[0].Prompt; got != body {
		t.Errorf("Prompt = %q, want %q", got, body)
	}
}

// TestInlinePromptFilesAbsoluteRejected pins that an absolute prompt_file path
// is rejected with AbsolutePromptFilePathError.
func TestInlinePromptFilesAbsoluteRejected(t *testing.T) {
	dir := t.TempDir()
	abs := writeFile(t, dir, "a.md", "x\n")
	wf := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt_file: ` + abs + `
`
	_, err := workflow.InlinePromptFiles([]byte(wf), dir)
	var got *workflow.AbsolutePromptFilePathError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As AbsolutePromptFilePathError failed; err = %v", err)
	}
	if got.Task != "a" {
		t.Errorf("AbsolutePromptFilePathError.Task = %q, want a", got.Task)
	}
}

// TestInlinePromptFilesMissing pins that an unreadable prompt_file yields a
// PromptFileError carrying the task id and path, wrapping os.ErrNotExist.
func TestInlinePromptFilesMissing(t *testing.T) {
	dir := t.TempDir()
	wf := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt_file: prompts/missing.md
`
	_, err := workflow.InlinePromptFiles([]byte(wf), dir)
	var got *workflow.PromptFileError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As PromptFileError failed; err = %v", err)
	}
	if got.Task != "a" {
		t.Errorf("PromptFileError.Task = %q, want a", got.Task)
	}
	if got.Path == "" {
		t.Errorf("PromptFileError.Path is empty")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("errors.Is(_, os.ErrNotExist) = false; err = %v", err)
	}
}

// TestInlinePromptFileConflicts pins mutual exclusivity: a prompt_file set
// alongside any other body form fails with TaskBodyConflictError whose Fields
// name both conflicting keys.
func TestInlinePromptFileConflicts(t *testing.T) {
	cases := map[string]struct {
		src   string
		other string
	}{
		"prompt": {
			other: "prompt",
			src: `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: x
    prompt_file: a.md
    prompt: inline
`,
		},
		"command": {
			other: "command",
			src: `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: x
    prompt_file: a.md
    command: echo hi
`,
		},
		"loop": {
			other: "loop",
			src: `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: x
    prompt_file: a.md
    loop:
      tasks:
        - id: y
          prompt: hi
`,
		},
		"for_each": {
			other: "for_each",
			src: `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: x
    prompt_file: a.md
    for_each:
      tasks:
        - id: y
          prompt: hi
`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.md", "body\n")
			_, err := workflow.InlinePromptFiles([]byte(tc.src), dir)
			var got *workflow.TaskBodyConflictError
			if !errors.As(err, &got) {
				t.Fatalf("errors.As TaskBodyConflictError failed; err = %v", err)
			}
			if got.Task != "x" {
				t.Errorf("TaskBodyConflictError.Task = %q, want x", got.Task)
			}
			if !slices.Contains(got.Fields, "prompt_file") {
				t.Errorf("Fields = %v, want to contain prompt_file", got.Fields)
			}
			if !slices.Contains(got.Fields, tc.other) {
				t.Errorf("Fields = %v, want to contain %q", got.Fields, tc.other)
			}
		})
	}
}

// TestParseFilePromptFile pins the end-to-end path: ParseFile reads a
// workflow.yaml referencing a prompt_file, inlines it relative to the file's
// directory, and Parse succeeds with the resolved prompt.
func TestParseFilePromptFile(t *testing.T) {
	dir := t.TempDir()
	const body = "do the thing\n"
	writeFile(t, dir, "prompts/a.md", body)
	path := writeFile(t, dir, "wf.yaml", `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt_file: prompts/a.md
`)
	wf, err := workflow.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got := wf.Tasks[0].Prompt; got != body {
		t.Errorf("Prompt = %q, want %q", got, body)
	}
}

// TestInlinePromptFilesNestedLoop pins that the recursive walk descends into a
// loop body's `tasks:` block: a prompt_file on a nested task is inlined the same
// way a top-level one is.
func TestInlinePromptFilesNestedLoop(t *testing.T) {
	dir := t.TempDir()
	const body = "inner loop body prompt\n"
	writeFile(t, dir, "prompts/inner.md", body)
	wf := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: go
  - id: lp
    loop:
      max: 2
      tasks:
        - id: inner
          prompt_file: prompts/inner.md
`
	out, err := workflow.InlinePromptFiles([]byte(wf), dir)
	if err != nil {
		t.Fatalf("InlinePromptFiles: %v", err)
	}
	if strings.Contains(string(out), "prompt_file") {
		t.Errorf("output still contains prompt_file:\n%s", out)
	}
	if !strings.Contains(string(out), body) {
		t.Errorf("output missing inlined body:\n%s", out)
	}
}

// TestInlinePromptFilesNoTrailingNewline pins the round-trip for file content
// that lacks a final newline: yaml.v3 emits a `|-` strip-chomped literal block,
// and Parse must recover the content byte-for-byte.
func TestInlinePromptFilesNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	const body = "no trailing newline here"
	writeFile(t, dir, "prompts/a.md", body)
	wf := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt_file: prompts/a.md
`
	out, err := workflow.InlinePromptFiles([]byte(wf), dir)
	if err != nil {
		t.Fatalf("InlinePromptFiles: %v", err)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(inlined): %v", err)
	}
	if got := parsed.Tasks[0].Prompt; got != body {
		t.Errorf("Prompt = %q, want %q", got, body)
	}
}

// TestInlinePromptFileLoopConflictSentinel pins that a loop wrapper carrying a
// prompt_file body is reported as a TaskBodyConflictError that satisfies
// errors.Is(_, ErrLoopTaskWithBody), the documented sentinel contract.
func TestInlinePromptFileLoopConflictSentinel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "body\n")
	wf := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: x
    prompt_file: a.md
    loop:
      tasks:
        - id: y
          prompt: hi
`
	_, err := workflow.InlinePromptFiles([]byte(wf), dir)
	if !errors.Is(err, workflow.ErrLoopTaskWithBody) {
		t.Fatalf("errors.Is(_, ErrLoopTaskWithBody) = false; err = %v", err)
	}
}

// TestParseFilePromptFilePlaceholderValidated pins that double-brace
// placeholders inside the loaded file are validated by Parse exactly as inline
// prompts are: a {{b}} reference not present in depends_on is rejected.
func TestParseFilePromptFilePlaceholderValidated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "prompts/a.md", "use {{b}} without declaring it\n")
	path := writeFile(t, dir, "wf.yaml", `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: b
    prompt: B
  - id: a
    prompt_file: prompts/a.md
`)
	_, err := workflow.ParseFile(path)
	var got *workflow.UnknownPlaceholderError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownPlaceholderError failed; err = %v", err)
	}
	if got.Task != "a" || got.Name != "b" {
		t.Errorf("UnknownPlaceholderError = %+v, want task=a name=b", got)
	}
}
