package main

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
runtime: cmd-echo
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

// writeFile drops body at dir/name and returns the absolute path.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// TestLinkSubWorkflows_RegistryName pins that linkSubWorkflows resolves a
// `workflow:` task that references a child by REGISTRY NAME, parses the child,
// and stores it in wf.Subs under the task id.
func TestLinkSubWorkflows_RegistryName(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	writeRegistryWorkflow(t, home, "release.yaml", childRelease)

	dir := t.TempDir()
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
	if err := linkSubWorkflows(wf, parentPath, nil); err != nil {
		t.Fatalf("linkSubWorkflows: %v", err)
	}
	child := wf.Subs["cut"]
	if child == nil {
		t.Fatal("wf.Subs[cut] = nil; want the resolved release child")
	}
	if child.ID != "release" {
		t.Errorf("child.ID = %q, want %q", child.ID, "release")
	}
}

// TestLinkSubWorkflows_PathRef pins that linkSubWorkflows resolves a `workflow:`
// task that references a child by PATH relative to the parent's directory.
func TestLinkSubWorkflows_PathRef(t *testing.T) {
	loomHomeForTest(t)
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
	if err := linkSubWorkflows(wf, parentPath, nil); err != nil {
		t.Fatalf("linkSubWorkflows: %v", err)
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
	loomHomeForTest(t)
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
	if err := linkSubWorkflows(wf, parentPath, nil); err != nil {
		t.Fatalf("linkSubWorkflows: %v", err)
	}
	child := wf.Subs["cut"]
	if child == nil {
		t.Fatal("wf.Subs[cut] = nil; want the linked child")
	}
	got := child.ByID("lint").Script
	if !filepath.IsAbs(got) {
		t.Errorf("child script path = %q, want an absolute anchored path", got)
	}
	if want := canonicalPath(scriptPath); canonicalPath(got) != want {
		t.Errorf("child script path = %q, want it anchored to %q", got, want)
	}
}

// TestLinkSubWorkflows_ModelOverride pins that runtime/model/effort set on a
// sub-workflow task override the linked child's workflow-level defaults: a child
// task that pins none of its own resolves to the override via Effective, so a
// parent can run a shared child on a cheaper model without forking it.
func TestLinkSubWorkflows_ModelOverride(t *testing.T) {
	loomHomeForTest(t)
	dir := t.TempDir()
	writeFile(t, dir, "child.yaml", `name: child
runtime: cmd-echo
model: opus
effort: high
tasks:
  - id: body
    prompt: hi
`)
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ./child.yaml
    model: sonnet
`)
	wf, err := workflow.ParseFile(parentPath)
	if err != nil {
		t.Fatalf("ParseFile parent: %v", err)
	}
	if err := linkSubWorkflows(wf, parentPath, nil); err != nil {
		t.Fatalf("linkSubWorkflows: %v", err)
	}
	child := wf.Subs["cut"]
	if child == nil {
		t.Fatal("wf.Subs[cut] = nil; want the linked child")
	}
	_, model, effort := child.Effective(child.ByID("body"))
	if model != "sonnet" {
		t.Errorf("child body model = %q, want the overridden %q", model, "sonnet")
	}
	if effort != "high" {
		t.Errorf("child body effort = %q, want the child's own %q (no override given)", effort, "high")
	}
}

// TestLinkSubWorkflows_Cycle pins that a link cycle (A links B, B links A) is
// detected and reported rather than recursing forever.
func TestLinkSubWorkflows_Cycle(t *testing.T) {
	loomHomeForTest(t)
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
	err = linkSubWorkflows(wf, aPath, nil)
	if err == nil {
		t.Fatal("linkSubWorkflows returned nil error for a A->B->A cycle; want a cycle error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "cycle") {
		t.Errorf("error %q does not mention a cycle", err.Error())
	}
}

// TestLinkSubWorkflows_UnreadableChildError characterizes the load-error message
// when a child path resolves to no file: linkSubWorkflows wraps the underlying
// read failure with the offending task id, and the wrapped os error stays
// reachable via errors.Is. (After unifying on readAndParse the message no longer
// carries the bespoke "read sub-workflow" phrasing.)
func TestLinkSubWorkflows_UnreadableChildError(t *testing.T) {
	loomHomeForTest(t)
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
	err = linkSubWorkflows(wf, parentPath, nil)
	if err == nil {
		t.Fatal("linkSubWorkflows returned nil error for an unreadable child; want error")
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
	loomHomeForTest(t)
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
	err = linkSubWorkflows(wf, parentPath, nil)
	if err == nil {
		t.Fatal("linkSubWorkflows returned nil error for a bad child prompt_file; want error")
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
// when a child fails to parse: the parse failure is wrapped with the task id and
// the child path.
func TestLinkSubWorkflows_MalformedChildError(t *testing.T) {
	loomHomeForTest(t)
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
	err = linkSubWorkflows(wf, parentPath, nil)
	if err == nil {
		t.Fatal("linkSubWorkflows returned nil error for a malformed child; want error")
	}
	if !strings.Contains(err.Error(), `task "cut"`) || !strings.Contains(err.Error(), childPath) {
		t.Errorf("error %q does not name the offending task and child path", err.Error())
	}
}

// TestLinkSubWorkflows_MissingChild pins that a `workflow:` ref that resolves to
// no file surfaces an error.
func TestLinkSubWorkflows_MissingChild(t *testing.T) {
	loomHomeForTest(t)
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
	if err := linkSubWorkflows(wf, parentPath, nil); err == nil {
		t.Fatal("linkSubWorkflows returned nil error for a missing child; want error")
	}
}

// TestLinkSubWorkflows_WithValueImplicitDep pins that a `{{task_id}}` reference
// inside a `with:` value contributes an implicit dependency at parse time: the
// sub-workflow task gains the referenced upstream task in its depends_on without
// an explicit depends_on entry, exercising buildSubWorkflowDeps end to end.
func TestLinkSubWorkflows_WithValueImplicitDep(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	writeRegistryWorkflow(t, home, "release.yaml", childRelease)

	dir := t.TempDir()
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
runtime: cmd-echo
model: m1
tasks:
  - id: seed
    prompt: "2.0.0"
  - id: cut
    workflow: release
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
// `loom run check` before any model call.
func TestCheckSubWorkflow_UnsatisfiedChildParams(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	writeRegistryWorkflow(t, home, "release.yaml", childRelease)

	dir := t.TempDir()
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: release
`)
	if out, err := runCLI(t, "run", "check", parentPath); err == nil {
		t.Fatalf("check accepted a sub-workflow missing a required child param; want error. output:\n%s", out)
	}
}

// TestCheckSubWorkflow_UnknownWithKey pins that a `with:` key that is not a
// declared child param fails the static check phase.
func TestCheckSubWorkflow_UnknownWithKey(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	writeRegistryWorkflow(t, home, "release.yaml", childRelease)

	dir := t.TempDir()
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: release
    with:
      version: "1.0.0"
      ghost: "x"
`)
	if out, err := runCLI(t, "run", "check", parentPath); err == nil {
		t.Fatalf("check accepted a with: key that is not a child param; want error. output:\n%s", out)
	}
}

// TestCheckSubWorkflow_AmbiguousOutput pins that a child with multiple terminal
// tasks and no explicit `output:` fails the static check phase (its result
// string is ambiguous).
func TestCheckSubWorkflow_AmbiguousOutput(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	writeRegistryWorkflow(t, home, "ambig.yaml", `name: ambig
runtime: cmd-echo
model: m1
tasks:
  - id: a
    prompt: hi
  - id: b
    prompt: bye
`)
	dir := t.TempDir()
	parentPath := writeFile(t, dir, "parent.yaml", `name: parent
tasks:
  - id: cut
    workflow: ambig
`)
	if out, err := runCLI(t, "run", "check", parentPath); err == nil {
		t.Fatalf("check accepted a child with an ambiguous output; want error. output:\n%s", out)
	}
}
