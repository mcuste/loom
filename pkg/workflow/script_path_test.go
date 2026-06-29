package workflow_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestAnchorScriptPathsRelative pins that a relative `script:` is rewritten to
// an absolute path anchored at baseDir, baked into the returned bytes so the
// manifest is self-contained.
func TestAnchorScriptPathsRelative(t *testing.T) {
	dir := t.TempDir()
	wf := `
name: wf
tasks:
  - id: a
    script: run.sh
`
	out, err := workflow.AnchorScriptPaths([]byte(wf), dir)
	if err != nil {
		t.Fatalf("AnchorScriptPaths: %v", err)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(anchored): %v", err)
	}
	absDir, _ := filepath.Abs(dir)
	want := filepath.Join(absDir, "run.sh")
	if got := parsed.Tasks[0].Script; got != want {
		t.Errorf("Script = %q, want %q", got, want)
	}
}

// TestAnchorScriptPathsLeavesAbsolute pins that an absolute `script:` is passed
// through untouched.
func TestAnchorScriptPathsLeavesAbsolute(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "tool.sh")
	wf := `
name: wf
tasks:
  - id: a
    script: ` + abs + `
`
	out, err := workflow.AnchorScriptPaths([]byte(wf), "/some/other/base")
	if err != nil {
		t.Fatalf("AnchorScriptPaths: %v", err)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(anchored): %v", err)
	}
	if got := parsed.Tasks[0].Script; got != abs {
		t.Errorf("Script = %q, want %q (unchanged)", got, abs)
	}
}

// TestAnchorScriptPathsLeavesTemplated pins that a `script:` path carrying a
// `{{...}}` template is left verbatim, so the dynamically built path is resolved
// after substitution at run time rather than having baseDir prepended.
func TestAnchorScriptPathsLeavesTemplated(t *testing.T) {
	wf := `
name: wf
params:
  - name: tool
tasks:
  - id: a
    script: "{{params.tool}}/run.sh"
`
	out, err := workflow.AnchorScriptPaths([]byte(wf), "/base")
	if err != nil {
		t.Fatalf("AnchorScriptPaths: %v", err)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(anchored): %v", err)
	}
	const want = "{{params.tool}}/run.sh"
	if got := parsed.Tasks[0].Script; got != want {
		t.Errorf("Script = %q, want %q (unchanged)", got, want)
	}
}

// TestAnchorScriptPathsNoScriptToken pins the fast path: bytes with no `script`
// token are returned unchanged (byte-identical, no YAML round-trip).
func TestAnchorScriptPathsNoScriptToken(t *testing.T) {
	wf := []byte("name: wf\ntasks:\n  - id: a\n    command: echo hi\n")
	out, err := workflow.AnchorScriptPaths(wf, "/base")
	if err != nil {
		t.Fatalf("AnchorScriptPaths: %v", err)
	}
	if string(out) != string(wf) {
		t.Errorf("fast path mutated bytes:\n got %q\nwant %q", out, wf)
	}
}

// TestParseFileAnchorsRelativeScript pins the end-to-end path: ParseFile anchors
// a relative `script:` to an absolute path under the workflow file's directory.
func TestParseFileAnchorsRelativeScript(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "run.sh", "#!/bin/sh\necho hi\n")
	yamlPath := writeFile(t, dir, "wf.yaml", `
name: wf
tasks:
  - id: a
    script: run.sh
`)
	wf, err := workflow.ParseFile(yamlPath)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := wf.Tasks[0].Script
	if !filepath.IsAbs(got) || !strings.HasSuffix(got, filepath.Join(filepath.Base(dir), "run.sh")) {
		t.Errorf("Script = %q, want absolute path ending in %q/run.sh", got, filepath.Base(dir))
	}
}
