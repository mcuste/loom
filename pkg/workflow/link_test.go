package workflow_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// childRelease is a valid child workflow: a required `version` param and an
// explicit `output:` selecting the publish sink. Used as the link target.
const childRelease = `name: release
runtime: test-rt
model: m1
output: publish
params:
  - name: version
    required: true
tasks:
  - id: build
    prompt: build {{params.version}}
  - id: publish
    depends_on: [build]
    prompt: publish {{build}}
`

// makeNamedResolver returns a SubRefResolver that maps known names to
// pre-seeded file paths and treats any other ref as a path (relative refs are
// anchored to parentDir, absolute refs are passed through).
func makeNamedResolver(names map[string]string) workflow.SubRefResolver {
	return func(ref, parentDir string) (string, error) {
		if p, ok := names[ref]; ok {
			return p, nil
		}
		if filepath.IsAbs(ref) {
			return ref, nil
		}
		return filepath.Join(parentDir, ref), nil
	}
}

// pathResolver is a SubRefResolver for tests that only use direct file paths:
// relative refs are anchored to parentDir, absolute refs pass through.
func pathResolver(ref, parentDir string) (string, error) {
	if filepath.IsAbs(ref) {
		return ref, nil
	}
	return filepath.Join(parentDir, ref), nil
}

// canonicalize reduces p to a stable absolute form: filepath.Abs then
// EvalSymlinks, falling back to the best form available on any step failure.
func canonicalize(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}

// TestLinkSubWorkflows_RegistryName pins that Link resolves a `workflow:` task
// that references a child by REGISTRY NAME (via the injected resolver), parses
// the child, and stores it in wf.Subs under the task id.
func TestLinkSubWorkflows_RegistryName(t *testing.T) {
	dir := t.TempDir()
	childPath := writeFile(t, dir, "release.yaml", childRelease)
	resolve := makeNamedResolver(map[string]string{"release": childPath})

	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: release
    with:
      version: "1.0.0"
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := workflow.Link(wf, parentPath, resolve); err != nil {
		t.Fatalf("Link: %v", err)
	}
	child := wf.Subs["cut"]
	if child == nil {
		t.Fatal("wf.Subs[cut] = nil; want the resolved release child")
	}
	if child.ID != "release" {
		t.Errorf("child.ID = %q, want %q", child.ID, "release")
	}
}

// TestLinkSubWorkflows_PathRef pins that Link resolves a `workflow:` task that
// references a child by PATH relative to the parent's directory.
func TestLinkSubWorkflows_PathRef(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "release.yaml", childRelease)
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ./release.yaml
    with:
      version: "1.0.0"
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := workflow.Link(wf, parentPath, pathResolver); err != nil {
		t.Fatalf("Link: %v", err)
	}
	if child := wf.Subs["cut"]; child == nil || child.ID != "release" {
		t.Errorf("wf.Subs[cut] = %+v, want resolved release child", child)
	}
}

// TestLinkSubWorkflows_AnchorsChildScriptPath pins that a relative `script:`
// path in a linked child is anchored to the CHILD's own directory, exactly as
// ParseFile anchors a top-level workflow. Without anchoring the child's
// `script: run.sh` would exec as a bare command and fail "not found in $PATH".
func TestLinkSubWorkflows_AnchorsChildScriptPath(t *testing.T) {
	parentDir := t.TempDir()
	childDir := t.TempDir()
	scriptPath := writeFile(t, childDir, "run.sh", "#!/usr/bin/env bash\n")
	childPath := writeFile(t, childDir, "child.yaml", `name: child
tasks:
  - id: lint
    script: run.sh
`)
	parentPath := writeFile(t, parentDir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: `+childPath+`
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := workflow.Link(wf, parentPath, pathResolver); err != nil {
		t.Fatalf("Link: %v", err)
	}
	child := wf.Subs["cut"]
	if child == nil {
		t.Fatal("wf.Subs[cut] = nil; want the linked child")
	}
	got := child.ByID("lint").Script
	if !filepath.IsAbs(got) {
		t.Errorf("child script path = %q, want an absolute anchored path", got)
	}
	if canonicalize(got) != canonicalize(scriptPath) {
		t.Errorf("child script path = %q, want it anchored to %q", got, scriptPath)
	}
}

// TestLinkSubWorkflows_ModelOverride pins that runtime/model/effort set on a
// sub-workflow task override the linked child's workflow-level defaults: a child
// task that pins none of its own resolves to the override via Effective, so a
// parent can run a shared child on a cheaper model without forking it.
func TestLinkSubWorkflows_ModelOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "child.yaml", `name: child
runtime: test-rt
model: m1
effort: high
tasks:
  - id: body
    prompt: hi
`)
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ./child.yaml
    model: m2
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := workflow.Link(wf, parentPath, pathResolver); err != nil {
		t.Fatalf("Link: %v", err)
	}
	child := wf.Subs["cut"]
	if child == nil {
		t.Fatal("wf.Subs[cut] = nil; want the linked child")
	}
	_, model, effort := child.Effective(child.ByID("body"))
	if model != "m2" {
		t.Errorf("child body model = %q, want the overridden %q", model, "m2")
	}
	if effort != "high" {
		t.Errorf("child body effort = %q, want the child's own %q (no override given)", effort, "high")
	}
}

// TestLinkSubWorkflows_Cycle pins that a link cycle (A links B, B links A) is
// detected and reported rather than recursing forever.
func TestLinkSubWorkflows_Cycle(t *testing.T) {
	dir := t.TempDir()
	aPath := writeFile(t, dir, "a.yaml", `name: awf
tasks:
  - id: callb
    workflow: ./b.yaml
`)
	writeFile(t, dir, "b.yaml", `name: bwf
tasks:
  - id: calla
    workflow: ./a.yaml
`)
	wf, err := workflow.ParseFile(aPath)
	if err != nil {
		t.Fatalf("ParseFile a: %v", err)
	}
	err = workflow.Link(wf, aPath, pathResolver)
	if err == nil {
		t.Fatal("Link returned nil error for an A->B->A cycle; want a cycle error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "cycle") {
		t.Errorf("error %q does not mention a cycle", err.Error())
	}
}

// TestLinkSubWorkflows_UnreadableChildError characterizes the load-error
// message when a child path resolves to no file: Link wraps the underlying
// read failure with the offending task id, and the wrapped os error stays
// reachable via errors.Is.
func TestLinkSubWorkflows_UnreadableChildError(t *testing.T) {
	dir := t.TempDir()
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ./does-not-exist.yaml
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	err = workflow.Link(wf, parentPath, pathResolver)
	if err == nil {
		t.Fatal("Link returned nil error for an unreadable child; want error")
	}
	if !strings.Contains(err.Error(), `task "cut"`) {
		t.Errorf("error %q does not name the offending task", err.Error())
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %q does not wrap os.ErrNotExist", err.Error())
	}
}

// TestLinkSubWorkflows_BadChildPromptFileError characterizes the load-error
// message when a child's `prompt_file:` cannot be read: the failure is wrapped
// with the task id, the child path, and stays reachable as a PromptFileError.
func TestLinkSubWorkflows_BadChildPromptFileError(t *testing.T) {
	dir := t.TempDir()
	childPath := writeFile(t, dir, "child.yaml", `name: child
tasks:
  - id: body
    prompt_file: missing.md
`)
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ./child.yaml
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	err = workflow.Link(wf, parentPath, pathResolver)
	if err == nil {
		t.Fatal("Link returned nil error for a bad child prompt_file; want error")
	}
	if !strings.Contains(err.Error(), `task "cut"`) || !strings.Contains(err.Error(), childPath) {
		t.Errorf("error %q does not name the offending task and child path", err.Error())
	}
	var pfErr *workflow.PromptFileError
	if !errors.As(err, &pfErr) {
		t.Errorf("error %q is not a PromptFileError", err.Error())
	}
}

// TestLinkSubWorkflows_MalformedChildError characterizes the load-error message
// when a child fails to parse: the parse failure is wrapped with the task id
// and the child path.
func TestLinkSubWorkflows_MalformedChildError(t *testing.T) {
	dir := t.TempDir()
	childPath := writeFile(t, dir, "child.yaml", "name: child\ntasks: [: not valid yaml\n")
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ./child.yaml
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	err = workflow.Link(wf, parentPath, pathResolver)
	if err == nil {
		t.Fatal("Link returned nil error for a malformed child; want error")
	}
	if !strings.Contains(err.Error(), `task "cut"`) || !strings.Contains(err.Error(), childPath) {
		t.Errorf("error %q does not name the offending task and child path", err.Error())
	}
}

// TestLinkSubWorkflows_MissingChild pins that a `workflow:` ref that resolves
// to no file surfaces an error.
func TestLinkSubWorkflows_MissingChild(t *testing.T) {
	dir := t.TempDir()
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ./does-not-exist.yaml
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := workflow.Link(wf, parentPath, pathResolver); err == nil {
		t.Fatal("Link returned nil error for a missing child; want error")
	}
}

// TestLinkSubWorkflows_WithValueImplicitDep pins that a `{{task_id}}` reference
// inside a `with:` value contributes an implicit dependency at parse time: the
// sub-workflow task gains the referenced upstream task in its depends_on without
// an explicit depends_on entry.
func TestLinkSubWorkflows_WithValueImplicitDep(t *testing.T) {
	dir := t.TempDir()
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: "2.0.0"
  - id: cut
    workflow: ./release.yaml
    with:
      version: "{{seed}}"
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	cut := wf.ByID("cut")
	if cut == nil {
		t.Fatal("wf.ByID(cut) = nil; want the sub-workflow task")
	}
	found := false
	for _, d := range cut.DependsOn {
		if d == "seed" {
			found = true
		}
	}
	if !found {
		t.Errorf("cut.DependsOn = %v, want it to include the implicit %q dep from the with: value", cut.DependsOn, "seed")
	}
}

// TestCheckSubWorkflow_UnsatisfiedChildParams pins the static check phase: a
// sub-workflow task whose `with:` does not cover a required child param fails
// Link before any model call.
func TestCheckSubWorkflow_UnsatisfiedChildParams(t *testing.T) {
	dir := t.TempDir()
	childPath := writeFile(t, dir, "release.yaml", childRelease)
	resolve := makeNamedResolver(map[string]string{"release": childPath})

	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: release
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := workflow.Link(wf, parentPath, resolve); err == nil {
		t.Fatal("Link accepted a sub-workflow missing a required child param; want error")
	}
}

// TestCheckSubWorkflow_UnknownWithKey pins that a `with:` key that is not a
// declared child param fails the static check phase.
func TestCheckSubWorkflow_UnknownWithKey(t *testing.T) {
	dir := t.TempDir()
	childPath := writeFile(t, dir, "release.yaml", childRelease)
	resolve := makeNamedResolver(map[string]string{"release": childPath})

	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: release
    with:
      version: "1.0.0"
      ghost: "x"
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := workflow.Link(wf, parentPath, resolve); err == nil {
		t.Fatal("Link accepted a with: key that is not a child param; want error")
	}
}

// TestCheckSubWorkflow_AmbiguousOutput pins that a child with multiple terminal
// tasks and no explicit `output:` fails the static check phase (its result
// string is ambiguous).
func TestCheckSubWorkflow_AmbiguousOutput(t *testing.T) {
	dir := t.TempDir()
	childPath := writeFile(t, dir, "ambig.yaml", `name: ambig
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: hi
  - id: b
    prompt: bye
`)
	resolve := makeNamedResolver(map[string]string{"ambig": childPath})

	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ambig
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := workflow.Link(wf, parentPath, resolve); err == nil {
		t.Fatal("Link accepted a child with an ambiguous output; want error")
	}
}
