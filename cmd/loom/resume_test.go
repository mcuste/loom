package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

// cmdFailRuntime always fails Run; the resume tests wire `a` to this runtime
// so the test can prove the executor bypassed `a` entirely. If `a` were
// re-dispatched, Run would return an error and the resume would fail.
type cmdFailRuntime struct{}

func (cmdFailRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdFailRuntime) Run(_ context.Context, _ runtime.Request) (runtime.Response, error) {
	return runtime.Response{}, errors.New("cmd-fail must never be dispatched")
}

func init() { runtime.Register("cmd-fail", cmdFailRuntime{}) }

// writeRunRecord drops a synthetic .loom/runs/<wfID>/<runID>.json under the
// current directory. The manifest field carries the workflow body verbatim
// (the resume command re-parses it). tasks is the per-task state mimicking
// the on-disk taskRecord shape; only `id`, `status`, `output`, and `prompt`
// are used by the seeding logic, so the test passes only those.
func writeRunRecord(t *testing.T, wfID, runID, manifest string, tasks []map[string]any, params map[string]string) string {
	t.Helper()
	dir := filepath.Join(".loom", "runs", wfID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	rec := map[string]any{
		"run_id":      runID,
		"workflow_id": wfID,
		"started_at":  "2026-06-09T14:30:52Z",
		"status":      "failed",
		"manifest":    manifest,
		"tasks":       tasks,
	}
	if params != nil {
		rec["params"] = params
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

// linkLatest creates the .loom/runs/<wfID>/latest.json symlink pointing at
// the named run file. Required for tests that resolve "latest".
func linkLatest(t *testing.T, wfID, runID string) {
	t.Helper()
	dir := filepath.Join(".loom", "runs", wfID)
	link := filepath.Join(dir, "latest.json")
	_ = os.Remove(link)
	if err := os.Symlink(runID+".json", link); err != nil {
		t.Fatalf("symlink latest: %v", err)
	}
}

// readNewRun finds the run record file under .loom/runs/<wfID> whose name is
// not skipID. Used to inspect what a fresh resume invocation wrote without
// having to predict its (timestamp + random) run id.
func readNewRun(t *testing.T, wfID, skipID string) map[string]any {
	t.Helper()
	dir := filepath.Join(".loom", "runs", wfID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "latest.json" || filepath.Ext(name) != ".json" {
			continue
		}
		if strings.TrimSuffix(name, ".json") == skipID {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read new run: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal new run: %v", err)
		}
		return m
	}
	t.Fatalf("no new run record produced under %s", dir)
	return nil
}

// TestResumeCommand_SeedsOkTasksAndRerunsFailed pins the contract: tasks with
// status="ok" in the record are seeded with their stored output so the
// executor never re-dispatches them, and downstream failed tasks re-run with
// the seeded value substituted into their prompts.
//
// `a` is wired to cmd-fail in the manifest: the resume would surface a non-nil
// error if it dispatched `a`. `b` is wired to cmd-echo so its prompt round-
// trips into Output. The new run record's b.prompt then proves the seed
// flowed through Substitute.
func TestResumeCommand_SeedsOkTasksAndRerunsFailed(t *testing.T) {
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-fail
    prompt: would-fail-if-rerun
  - id: b
    runtime: cmd-echo
    depends_on: [a]
    prompt: "got: {{a}}"
`
	runID := "20260101T000000Z-aaaaaa"
	writeRunRecord(t, "wf", runID, manifest, []map[string]any{
		{"id": "a", "status": "ok", "output": "stored-a", "prompt": "would-fail-if-rerun"},
		{"id": "b", "status": "failed", "error": "kaboom"},
	}, nil)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"resume", runID})

	if err := root.Execute(); err != nil {
		t.Fatalf("resume: %v\noutput:\n%s", err, buf.String())
	}

	rec := readNewRun(t, "wf", runID)
	tasks, _ := rec["tasks"].([]any)
	var bPrompt string
	for _, raw := range tasks {
		entry := raw.(map[string]any)
		if entry["id"] == "b" {
			bPrompt, _ = entry["prompt"].(string)
		}
	}
	if bPrompt != "got: stored-a" {
		t.Errorf("b.prompt = %q, want %q (seed of a did not feed downstream)", bPrompt, "got: stored-a")
	}
}

// TestResumeCommand_LatestResolvesSymlink pins that `loom resume latest`
// follows .loom/runs/<wf>/latest.json instead of treating "latest" as a
// literal run id. Without this, users would have to copy-paste the random
// suffix every time they wanted to retry the most recent run.
func TestResumeCommand_LatestResolvesSymlink(t *testing.T) {
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-fail
    prompt: would-fail-if-rerun
  - id: b
    runtime: cmd-echo
    depends_on: [a]
    prompt: "got: {{a}}"
`
	runID := "20260101T000000Z-bbbbbb"
	writeRunRecord(t, "wf", runID, manifest, []map[string]any{
		{"id": "a", "status": "ok", "output": "stored-a", "prompt": "would-fail-if-rerun"},
		{"id": "b", "status": "failed", "error": "kaboom"},
	}, nil)
	linkLatest(t, "wf", runID)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"resume", "latest"})

	if err := root.Execute(); err != nil {
		t.Fatalf("resume latest: %v\noutput:\n%s", err, buf.String())
	}
}

// TestRunCommand_ResumeLatestFlag pins the alternate entry point:
// `loom run wf.yaml --resume-latest` loads .loom/runs/<wf>/latest.json,
// seeds ok tasks with their stored output, and runs only the remainder.
// The workflow YAML on disk supplies the manifest (the run record is only
// consulted for the seeded outputs and the original params).
func TestRunCommand_ResumeLatestFlag(t *testing.T) {
	dir := t.TempDir()
	chdirTo(t, dir)

	wfBody := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-fail
    prompt: would-fail-if-rerun
  - id: b
    runtime: cmd-echo
    depends_on: [a]
    prompt: "got: {{a}}"
`
	wfPath := writeWorkflow(t, wfBody)
	// writeWorkflow uses its own TempDir; copy into the cwd so the run still
	// finds the workflow file via the supplied path.
	_ = wfPath // path is absolute so cwd is irrelevant.

	runID := "20260101T000000Z-cccccc"
	writeRunRecord(t, "wf", runID, wfBody, []map[string]any{
		{"id": "a", "status": "ok", "output": "stored-a", "prompt": "would-fail-if-rerun"},
		{"id": "b", "status": "failed", "error": "kaboom"},
	}, nil)
	linkLatest(t, "wf", runID)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", wfPath, "--resume-latest"})

	if err := root.Execute(); err != nil {
		t.Fatalf("run --resume-latest: %v\noutput:\n%s", err, buf.String())
	}

	rec := readNewRun(t, "wf", runID)
	tasks, _ := rec["tasks"].([]any)
	var bPrompt string
	for _, raw := range tasks {
		entry := raw.(map[string]any)
		if entry["id"] == "b" {
			bPrompt, _ = entry["prompt"].(string)
		}
	}
	if bPrompt != "got: stored-a" {
		t.Errorf("b.prompt = %q, want %q (resume-latest did not seed a)", bPrompt, "got: stored-a")
	}
}

// TestResumeCommand_DropsSeedForIDsRemovedFromWorkflow pins the workflow-
// evolution case: an id that was ok in the original record but is no longer
// present in the current YAML must be dropped from the seed entirely. The
// resume must (a) not produce a negative progress denominator, (b) not stamp
// the absent id into the new run record (it would mislead a future resume),
// and (c) succeed end-to-end by running only the tasks the new YAML declares.
func TestResumeCommand_DropsSeedForIDsRemovedFromWorkflow(t *testing.T) {
	chdirTo(t, t.TempDir())

	// Original record has `a` (ok) and `b` (failed, depended on `a`); the
	// current workflow has only `b` and it depends on nothing.
	origManifest := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-echo
    prompt: x
  - id: b
    runtime: cmd-echo
    depends_on: [a]
    prompt: "saw {{a}}"
`
	currentManifest := `name: wf
model: m1
tasks:
  - id: b
    runtime: cmd-echo
    prompt: standalone
`
	runID := "20260101T000000Z-eeeeee"
	// Store the *current* manifest in the record so the resume reparses the
	// up-to-date YAML; the record still lists the legacy task `a`.
	writeRunRecord(t, "wf", runID, currentManifest, []map[string]any{
		{"id": "a", "status": "ok", "output": "stored-a"},
		{"id": "b", "status": "failed", "error": "kaboom"},
	}, nil)
	_ = origManifest

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"resume", runID})

	if err := root.Execute(); err != nil {
		t.Fatalf("resume: %v\noutput:\n%s", err, buf.String())
	}

	// The progress lines must not show a negative or zero denominator.
	out := buf.String()
	if strings.Contains(out, "/-") || strings.Contains(out, "/0]") {
		t.Errorf("progress line carries a non-positive denominator:\n%s", out)
	}

	// The new run record must contain `b` (re-run) and NOT contain `a`
	// (dropped: not in the current workflow).
	rec := readNewRun(t, "wf", runID)
	tasks, _ := rec["tasks"].([]any)
	var sawA, sawB bool
	for _, raw := range tasks {
		entry := raw.(map[string]any)
		switch entry["id"] {
		case "a":
			sawA = true
		case "b":
			sawB = true
		}
	}
	if sawA {
		t.Errorf("new run record contains task `a`, but `a` was removed from the workflow")
	}
	if !sawB {
		t.Errorf("new run record is missing task `b`, which should have re-run")
	}
}

// TestResumeCommand_ReusesOriginalParams pins that param values from the
// original run record carry into the resume invocation without the caller
// having to re-supply them. The workflow declares a required param; the
// record stores params={"who":"alice"}; the resume command is called with
// no -p flag and must succeed.
func TestResumeCommand_ReusesOriginalParams(t *testing.T) {
	chdirTo(t, t.TempDir())

	manifest := `name: wf
runtime: cmd-echo
model: m1
params:
  - name: who
    required: true
tasks:
  - id: a
    prompt: "hello {{params.who}}"
  - id: b
    depends_on: [a]
    prompt: "echo: {{a}}"
`
	runID := "20260101T000000Z-dddddd"
	writeRunRecord(t, "wf", runID, manifest, []map[string]any{
		{"id": "a", "status": "ok", "output": "hello alice", "prompt": "hello alice"},
		{"id": "b", "status": "failed", "error": "kaboom"},
	}, map[string]string{"who": "alice"})

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"resume", runID})

	if err := root.Execute(); err != nil {
		t.Fatalf("resume (no -p): %v\noutput:\n%s", err, buf.String())
	}

	rec := readNewRun(t, "wf", runID)
	params, ok := rec["params"].(map[string]any)
	if !ok {
		t.Fatalf("new run record has no params: %v", rec["params"])
	}
	if params["who"] != "alice" {
		t.Errorf("params[who] = %v, want alice (original params not reused)", params["who"])
	}
}
