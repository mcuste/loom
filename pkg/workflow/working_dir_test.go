package workflow_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestAnchorWorkingDirRelative pins that a relative `working_dir:` is rewritten
// to an absolute path anchored at baseDir, baked into the returned bytes so the
// manifest names a fixed cwd regardless of where it is later parsed.
func TestAnchorWorkingDirRelative(t *testing.T) {
	dir := t.TempDir()
	wf := `
name: wf
working_dir: sub/dir
tasks:
  - id: a
    command: pwd
`
	out, err := workflow.AnchorWorkingDir([]byte(wf), dir)
	if err != nil {
		t.Fatalf("AnchorWorkingDir: %v", err)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(anchored): %v", err)
	}
	absDir, _ := filepath.Abs(dir)
	want := filepath.Join(absDir, "sub/dir")
	if got := parsed.WorkingDir; got != want {
		t.Errorf("WorkingDir = %q, want %q", got, want)
	}
}

// TestAnchorWorkingDirLeavesAbsolute pins that an absolute `working_dir:` is
// passed through untouched.
func TestAnchorWorkingDirLeavesAbsolute(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "root")
	wf := `
name: wf
working_dir: ` + abs + `
tasks:
  - id: a
    command: pwd
`
	out, err := workflow.AnchorWorkingDir([]byte(wf), "/some/other/base")
	if err != nil {
		t.Fatalf("AnchorWorkingDir: %v", err)
	}
	parsed, err := workflow.Parse(out)
	if err != nil {
		t.Fatalf("Parse(anchored): %v", err)
	}
	if got := parsed.WorkingDir; got != abs {
		t.Errorf("WorkingDir = %q, want %q (unchanged)", got, abs)
	}
}

// TestAnchorWorkingDirLeavesTemplated pins that a `working_dir:` carrying a
// `{{...}}` template is left verbatim, so it resolves after substitution rather
// than having baseDir prepended to a half-formed value.
func TestAnchorWorkingDirLeavesTemplated(t *testing.T) {
	wf := `
name: wf
working_dir: "{{params.root}}/repo"
tasks:
  - id: a
    command: pwd
`
	out, err := workflow.AnchorWorkingDir([]byte(wf), "/base")
	if err != nil {
		t.Fatalf("AnchorWorkingDir: %v", err)
	}
	// Assert on the anchored bytes directly: a templated value must survive
	// verbatim and never have baseDir prepended.
	if !strings.Contains(string(out), "{{params.root}}/repo") {
		t.Errorf("anchored bytes dropped the template:\n%s", out)
	}
	if strings.Contains(string(out), "/base") {
		t.Errorf("anchored bytes prepended baseDir to a templated value:\n%s", out)
	}
}

// TestAnchorWorkingDirNoToken pins the fast path: bytes with no `working_dir`
// token are returned unchanged (byte-identical, no YAML round-trip).
func TestAnchorWorkingDirNoToken(t *testing.T) {
	wf := []byte("name: wf\ntasks:\n  - id: a\n    command: echo hi\n")
	out, err := workflow.AnchorWorkingDir(wf, "/base")
	if err != nil {
		t.Fatalf("AnchorWorkingDir: %v", err)
	}
	if string(out) != string(wf) {
		t.Errorf("fast path mutated bytes:\n got %q\nwant %q", out, wf)
	}
}

// TestParseFileAnchorsRelativeWorkingDir pins the end-to-end path: ParseFile
// anchors a relative `working_dir:` to an absolute path under the workflow
// file's directory.
func TestParseFileAnchorsRelativeWorkingDir(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writeFile(t, dir, "wf.yaml", `
name: wf
working_dir: ..
tasks:
  - id: a
    command: pwd
`)
	wf, err := workflow.ParseFile(yamlPath)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	got := wf.WorkingDir
	want := filepath.Dir(dir) // ".." from the yaml's dir
	if !filepath.IsAbs(got) || filepath.Clean(got) != filepath.Clean(want) {
		t.Errorf("WorkingDir = %q, want %q (parent of %q)", got, want, dir)
	}
}
