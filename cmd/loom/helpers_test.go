package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeWorkflow drops a workflow YAML into t.TempDir() and returns the path.
func writeWorkflow(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

// chdirTo cd's into dir for the rest of the test, restoring the original
// cwd via t.Cleanup. e2e tests pair this with loomHomeForTest so the store
// roots under an isolated $LOOM_HOME and the run's recorded cwd is the temp
// dir rather than the test process's working directory.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// loomHomeForTest creates a temp dir, points LOOM_HOME at it for the rest of
// the test, and returns the dir. e2e tests call it alongside chdirTo so the
// store writes under an isolated home rather than ./.loom (or, worse, the real
// $HOME/.loom once home resolution lands).
func loomHomeForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOOM_HOME", dir)
	return dir
}

// testRunsDir returns the runs root the store writes under: $LOOM_HOME/runs
// when LOOM_HOME is set (every e2e test sets it via loomHomeForTest), else the
// legacy ./.loom/runs fallback so helpers still work in tests that don't.
func testRunsDir(t *testing.T) string {
	t.Helper()
	if home := os.Getenv("LOOM_HOME"); home != "" {
		return filepath.Join(home, "runs")
	}
	return filepath.Join(".loom", "runs")
}
