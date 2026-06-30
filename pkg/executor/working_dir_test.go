package executor_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/workflow"
)

// wantCwd resolves dir to the physical path `pwd` (getcwd) reports, so the
// comparison holds on macOS where t.TempDir() lives under a /var -> /private/var
// symlink.
func wantCwd(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return resolved
}

// TestWorkingDirShell pins that a workflow's working_dir becomes the cwd of a
// shell task's child process: `pwd` reports the configured directory, not the
// process cwd the test runs in.
func TestWorkingDirShell(t *testing.T) {
	dir := t.TempDir()
	wf := &workflow.Workflow{
		ID:         "wf",
		WorkingDir: dir,
		Tasks:      []workflow.Task{{ID: "where", Command: "pwd"}},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["where"]; got != wantCwd(t, dir) {
		t.Errorf("Outputs[where] = %q, want %q (the workflow working_dir)", got, wantCwd(t, dir))
	}
}

// TestWorkingDirPropagatesToSubWorkflow pins that a parent's working_dir is
// inherited by a linked child's tasks: the child's shell task runs in the
// parent's directory even though the child declares none of its own.
func TestWorkingDirPropagatesToSubWorkflow(t *testing.T) {
	dir := t.TempDir()
	child := &workflow.Workflow{
		ID:     "child",
		Output: "where",
		Tasks:  []workflow.Task{{ID: "where", Command: "pwd"}},
	}
	parent := &workflow.Workflow{
		ID:         "parent",
		WorkingDir: dir,
		Tasks:      []workflow.Task{{ID: "call", Workflow: "child"}},
		Subs:       map[workflow.TaskID]*workflow.Workflow{"call": child},
	}
	rep, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["call"]; got != wantCwd(t, dir) {
		t.Errorf("Outputs[call] = %q, want %q (inherited from parent working_dir)", got, wantCwd(t, dir))
	}
}

// TestWorkingDirChildOverridesParent pins the precedence: a linked child that
// sets its own working_dir runs there, not in the parent's directory.
func TestWorkingDirChildOverridesParent(t *testing.T) {
	parentDir := t.TempDir()
	childDir := t.TempDir()
	child := &workflow.Workflow{
		ID:         "child",
		WorkingDir: childDir,
		Output:     "where",
		Tasks:      []workflow.Task{{ID: "where", Command: "pwd"}},
	}
	parent := &workflow.Workflow{
		ID:         "parent",
		WorkingDir: parentDir,
		Tasks:      []workflow.Task{{ID: "call", Workflow: "child"}},
		Subs:       map[workflow.TaskID]*workflow.Workflow{"call": child},
	}
	rep, err := executor.Run(context.Background(), parent, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["call"]; got != wantCwd(t, childDir) {
		t.Errorf("Outputs[call] = %q, want %q (child's own working_dir wins)", got, wantCwd(t, childDir))
	}
}
