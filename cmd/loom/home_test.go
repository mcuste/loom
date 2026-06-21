package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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

// TestLoomHome_HonorsEnvAndCreatesDir pins that loomHome returns $LOOM_HOME
// verbatim when it is set and creates the directory if it does not yet exist.
func TestLoomHome_HonorsEnvAndCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom-home")
	t.Setenv("LOOM_HOME", dir)

	got, err := loomHome()
	if err != nil {
		t.Fatalf("loomHome: %v", err)
	}
	if got != dir {
		t.Errorf("loomHome = %q, want %q", got, dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("loom home dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("loom home %q is not a directory", dir)
	}
}

// TestLoomHome_ResolvesRelativeEnvToAbs pins that a relative LOOM_HOME is
// resolved to an absolute path (via filepath.Abs) before use, so the two
// loomHome calls that straddle a resume-time chdir agree on one on-disk
// location instead of silently splitting the store.
func TestLoomHome_ResolvesRelativeEnvToAbs(t *testing.T) {
	chdirTo(t, t.TempDir())
	t.Setenv("LOOM_HOME", "rel-home")

	got, err := loomHome()
	if err != nil {
		t.Fatalf("loomHome: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("loomHome = %q, want an absolute path", got)
	}
	// filepath.Abs resolves against os.Getwd(); derive want the same way so the
	// comparison holds even where the temp dir is a symlink (e.g. macOS /var).
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	want := filepath.Join(cwd, "rel-home")
	if got != want {
		t.Errorf("loomHome = %q, want %q", got, want)
	}
	if info, err := os.Stat(got); err != nil {
		t.Fatalf("relative loom home dir not created: %v", err)
	} else if !info.IsDir() {
		t.Errorf("loom home %q is not a directory", got)
	}
}

// TestLoomHome_FallsBackToUserHomeDir pins that with LOOM_HOME unset, loomHome
// resolves $HOME/.loom and creates it.
func TestLoomHome_FallsBackToUserHomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", "")
	t.Setenv("HOME", home)

	got, err := loomHome()
	if err != nil {
		t.Fatalf("loomHome: %v", err)
	}
	want := filepath.Join(home, ".loom")
	if got != want {
		t.Errorf("loomHome = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("fallback home dir not created: %v", err)
	}
}

// TestRunCommand_RecordsInvocationCwd pins that a run records the directory it
// was invoked from in the run record's `cwd` field, so a later resume can
// restore it.
func TestRunCommand_RecordsInvocationCwd(t *testing.T) {
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	path := writeWorkflow(t, `
name: wf
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello
`)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", path})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, buf.String())
	}

	data, err := os.ReadFile(filepath.Join(testRunsDir(t), "wf", "latest.json"))
	if err != nil {
		t.Fatalf("read latest.json: %v", err)
	}
	var record struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal run record: %v\nraw:\n%s", err, data)
	}
	if record.Cwd != cwd {
		t.Errorf("record.cwd = %q, want %q (invocation cwd not recorded)", record.Cwd, cwd)
	}
}

// TestResumeCommand_ChdirsToRecordedCwd pins that `loom resume <id>` changes
// into the run record's recorded cwd before re-running, so a re-run shell
// task's relative writes land in the original directory rather than the
// directory the resume was launched from.
func TestResumeCommand_ChdirsToRecordedCwd(t *testing.T) {
	loomHomeForTest(t)
	recordedCwd := t.TempDir()
	invocationCwd := t.TempDir()
	chdirTo(t, invocationCwd)

	manifest := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-echo
    prompt: x
  - id: write
    depends_on: [a]
    command: 'echo hi > marker.txt'
`
	runID := "20260101T000000Z-ffffff"
	writeRunRecordWithCwd(t, "wf", runID, manifest, recordedCwd, []map[string]any{
		{"id": "a", "status": "ok", "output": "stored-a", "prompt": "x"},
		{"id": "write", "status": "failed", "error": "kaboom"},
	})

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"resume", runID})
	if err := root.Execute(); err != nil {
		t.Fatalf("resume: %v\noutput:\n%s", err, buf.String())
	}

	if _, err := os.Stat(filepath.Join(recordedCwd, "marker.txt")); err != nil {
		t.Errorf("re-run shell task did not write into the recorded cwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(invocationCwd, "marker.txt")); err == nil {
		t.Errorf("re-run shell task wrote into the invocation cwd; resume did not chdir to the recorded cwd")
	}
}

// writeRunRecordWithCwd drops a synthetic run record under the test's runs
// root with an explicit `cwd` field, used by the resume-chdir test. It mirrors
// writeRunRecord but adds the cwd the original run was invoked from.
func writeRunRecordWithCwd(t *testing.T, wfID, runID, manifest, cwd string, tasks []map[string]any) string {
	t.Helper()
	dir := filepath.Join(testRunsDir(t), wfID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	rec := map[string]any{
		"run_id":      runID,
		"workflow_id": wfID,
		"started_at":  "2026-06-09T14:30:52Z",
		"status":      "failed",
		"manifest":    manifest,
		"cwd":         cwd,
		"tasks":       tasks,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	path := filepath.Join(dir, runID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write record: %v", err)
	}
	return path
}
