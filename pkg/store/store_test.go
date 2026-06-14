package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// fixedClock returns a deterministic timestamp so tests can pin the run id
// without depending on wall-clock behavior.
func fixedClock(ts string) func() time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

// counterRand returns a deterministic six-hex suffix. Tests use it to assert
// that the run id is "<clock-prefix>-<suffix>".
func counterRand(initial uint32) func() (string, error) {
	var n atomic.Uint32
	n.Store(initial)
	return func() (string, error) {
		v := n.Add(1) - 1
		return fmt.Sprintf("%06x", v), nil
	}
}

// readRun decodes the run JSON file at path into a loose map. Tests assert
// against map keys so the test does not duplicate the on-disk DTO struct.
func readRun(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// TestOpenWritesInitialRunFile pins the on-disk layout produced by Open:
// the file path is "<root>/runs/<wf>/<run_id>.json", the run id is
// "<ts>-<hex>", and the file already contains the embedded manifest plus a
// status of "running" so an external observer can tell the run is in flight.
func TestOpenWritesInitialRunFile(t *testing.T) {
	root := t.TempDir()
	manifest := []byte("name: wf\nruntime: x\nmodel: m\ntasks: []\n")

	run, err := store.Open("wf", manifest, store.Config{
		Root: root,
		Now:  fixedClock("2026-06-09T14:30:52Z"),
		Rand: counterRand(0xa1b2c3),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	wantID := "20260609T143052Z-a1b2c3"
	if run.ID() != wantID {
		t.Fatalf("ID = %q, want %q", run.ID(), wantID)
	}
	wantPath := filepath.Join(root, "runs", "wf", wantID+".json")
	if run.Path() != wantPath {
		t.Fatalf("Path = %q, want %q", run.Path(), wantPath)
	}

	m := readRun(t, wantPath)
	if m["run_id"] != wantID || m["workflow_id"] != "wf" {
		t.Errorf("run_id/workflow_id = %v/%v", m["run_id"], m["workflow_id"])
	}
	if m["status"] != "running" {
		t.Errorf("status = %v, want running", m["status"])
	}
	if m["manifest"] != string(manifest) {
		t.Errorf("manifest mismatch:\n got=%q\nwant=%q", m["manifest"], manifest)
	}
}

// TestOnStartOnFinishUpdatesTaskEntry exercises the full hook cycle: a single
// task entry must accumulate routing fields (OnStart) and then prompt,
// output, usage, and timing (OnFinish) without losing data from the first
// write. This is the single-JSON equivalent of the meta-merge contract.
func TestOnStartOnFinishUpdatesTaskEntry(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{
		Root: root,
		Now:  fixedClock("2026-06-09T14:30:52Z"),
		Rand: counterRand(1),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	task := workflow.Task{ID: "alpha"}
	run.OnStart()(task, "claude-code", "sonnet", "medium")
	run.OnFinish()(task, store.TaskResult{
		Prompt:  "substituted prompt",
		Output:  "model output",
		Usage:   runtime.Usage{InputTokens: 10, OutputTokens: 20, TotalCostUSD: 0.5},
		Elapsed: 250 * time.Millisecond,
	}, nil)

	m := readRun(t, run.Path())
	tasks, ok := m["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks = %v, want exactly 1 entry", m["tasks"])
	}
	got := tasks[0].(map[string]any)
	for k, want := range map[string]any{
		"id":      "alpha",
		"runtime": "claude-code",
		"model":   "sonnet",
		"effort":  "medium",
		"status":  "ok",
		"prompt":  "substituted prompt",
		"output":  "model output",
	} {
		if got[k] != want {
			t.Errorf("tasks[0][%q] = %v, want %v", k, got[k], want)
		}
	}
	if v, _ := got["elapsed_ms"].(float64); int64(v) != 250 {
		t.Errorf("elapsed_ms = %v, want 250", got["elapsed_ms"])
	}
	usage := got["usage"].(map[string]any)
	if v, _ := usage["input_tokens"].(float64); int(v) != 10 {
		t.Errorf("usage.input_tokens = %v, want 10", usage["input_tokens"])
	}
}

// TestOnFinishRecordsTaskError pins that a failed task surfaces both
// status="failed" and the error message in its task entry — needed for
// post-mortem inspection of partial runs.
func TestOnFinishRecordsTaskError(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	task := workflow.Task{ID: "beta"}
	run.OnStart()(task, "rt", "m", "")
	run.OnFinish()(task, store.TaskResult{}, errors.New("kaboom"))

	tasks := readRun(t, run.Path())["tasks"].([]any)
	got := tasks[0].(map[string]any)
	if got["status"] != "failed" || got["error"] != "kaboom" {
		t.Fatalf("task = %v, want status=failed error=kaboom", got)
	}
}

// TestCloseFinalizesAndLinksLatest pins the contract of Close: top-level
// status/usage/task_count get populated, and latest.json points at this
// run's file so "open the most recent" is a one-step lookup.
func TestCloseFinalizesAndLinksLatest(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{
		Root: root,
		Now:  fixedClock("2026-06-09T14:30:52Z"),
		Rand: counterRand(0xa1b2c3),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	run.OnFinish()(workflow.Task{ID: "a"}, store.TaskResult{Elapsed: 10 * time.Millisecond}, nil)
	run.OnFinish()(workflow.Task{ID: "b"}, store.TaskResult{Elapsed: 20 * time.Millisecond}, nil)

	summary := &store.Summary{
		TaskCount: 2,
		Usage:     runtime.Usage{InputTokens: 100, OutputTokens: 200, TotalCostUSD: 1.5},
	}
	if err := run.Close(summary, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}

	m := readRun(t, run.Path())
	if m["status"] != "ok" {
		t.Errorf("status = %v, want ok", m["status"])
	}
	if v, _ := m["task_count"].(float64); int(v) != 2 {
		t.Errorf("task_count = %v, want 2", m["task_count"])
	}
	usage := m["usage"].(map[string]any)
	if v, _ := usage["total_cost_usd"].(float64); v != 1.5 {
		t.Errorf("usage.total_cost_usd = %v, want 1.5", usage["total_cost_usd"])
	}

	target, err := os.Readlink(filepath.Join(root, "runs", "wf", "latest.json"))
	if err != nil {
		t.Fatalf("readlink latest.json: %v", err)
	}
	if target != run.ID()+".json" {
		t.Fatalf("latest.json -> %q, want %q", target, run.ID()+".json")
	}
}

// TestCloseFailedRun pins that a non-nil run error surfaces in the summary,
// so a partial run can be distinguished from a clean one without inspecting
// individual task entries.
func TestCloseFailedRun(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := run.Close(&store.Summary{}, errors.New("boom")); err != nil {
		t.Fatalf("Close: %v", err)
	}
	m := readRun(t, run.Path())
	if m["status"] != "failed" || m["error"] != "boom" {
		t.Fatalf("summary = %v, want status=failed error=boom", m)
	}
}

// TestAtomicRewriteLeavesNoTmp pins the crash-safety property: each flush
// writes via "<path>.tmp" then renames, so the canonical path is always a
// valid JSON document and no .tmp residue accumulates between writes.
func TestAtomicRewriteLeavesNoTmp(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := range 5 {
		task := workflow.Task{ID: workflow.TaskID(fmt.Sprintf("t%d", i))}
		run.OnStart()(task, "rt", "m", "")
		run.OnFinish()(task, store.TaskResult{}, nil)
	}
	if _, err := os.Stat(run.Path() + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tmp file lingered: err=%v", err)
	}
	// Canonical file still parses cleanly.
	if _ = readRun(t, run.Path()); t.Failed() {
		return
	}
}

// TestConcurrentOnFinishIsSafe is the smoke test for the concurrent-write
// path: N parallel OnFinish calls must not race on the in-memory state nor
// stomp each other's writes. The executor dispatches finish hooks
// concurrently for independent tasks, so this is the contract the store
// has to honor.
func TestConcurrentOnFinishIsSafe(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func() {
			defer wg.Done()
			id := workflow.TaskID(fmt.Sprintf("t%02d", i))
			task := workflow.Task{ID: id}
			run.OnStart()(task, "rt", "m", "")
			run.OnFinish()(task, store.TaskResult{Output: fmt.Sprintf("out-%d", i)}, nil)
		}()
	}
	wg.Wait()
	if err := run.Close(&store.Summary{}, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}

	tasks := readRun(t, run.Path())["tasks"].([]any)
	if len(tasks) != N {
		t.Fatalf("tasks len = %d, want %d", len(tasks), N)
	}
	seen := map[string]string{}
	for _, raw := range tasks {
		entry := raw.(map[string]any)
		seen[entry["id"].(string)] = entry["output"].(string)
	}
	for i := range N {
		id := fmt.Sprintf("t%02d", i)
		want := fmt.Sprintf("out-%d", i)
		if seen[id] != want {
			t.Fatalf("output[%s] = %q, want %q", id, seen[id], want)
		}
	}
}

// TestRunRecordIncludesParams pins that params passed via Config appear under
// the "params" key in the on-disk JSON so post-run inspection can show what
// values were in effect.
func TestRunRecordIncludesParams(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{
		Root:   root,
		Params: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	m := readRun(t, run.Path())
	params, ok := m["params"].(map[string]any)
	if !ok {
		t.Fatalf("params not a map: %v", m["params"])
	}
	if params["env"] != "prod" {
		t.Errorf("params[env] = %v, want prod", params["env"])
	}
}

// TestEmptyParamsOmitted pins the omitempty contract: a run opened with no
// Params must not produce a "params" key in the JSON so existing consumers
// that parse the file as a loose map are unaffected.
func TestEmptyParamsOmitted(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	m := readRun(t, run.Path())
	if _, present := m["params"]; present {
		t.Errorf("params key present in JSON but expected absent")
	}
}

// TestShellTaskCommandRoundTrips pins that a TaskResult with Command set
// persists the "command" field to disk, and that an LLM task with only Prompt
// continues to round-trip correctly. Both tasks are exercised in the same run
// so the test also confirms the two record shapes coexist without collision.
func TestShellTaskCommandRoundTrips(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Shell task: Command holds original body; Prompt holds substituted cmdline;
	// no runtime/model/effort (executor passes empty strings for shell tasks).
	shellTask := workflow.Task{ID: "shell-echo"}
	run.OnStart()(shellTask, "", "", "")
	run.OnFinish()(shellTask, store.TaskResult{
		Prompt:  "echo hello world",
		Command: "echo {{msg}}",
		Output:  "hello world\n",
		Elapsed: 50 * time.Millisecond,
	}, nil)

	// LLM task: only Prompt set; Command must stay absent in JSON.
	llmTask := workflow.Task{ID: "llm-summarise"}
	run.OnStart()(llmTask, "claude-code", "sonnet", "low")
	run.OnFinish()(llmTask, store.TaskResult{
		Prompt:  "summarise this",
		Output:  "summary here",
		Usage:   runtime.Usage{InputTokens: 5, OutputTokens: 3},
		Elapsed: 120 * time.Millisecond,
	}, nil)

	tasks := readRun(t, run.Path())["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("tasks len = %d, want 2", len(tasks))
	}

	byID := map[string]map[string]any{}
	for _, raw := range tasks {
		entry := raw.(map[string]any)
		byID[entry["id"].(string)] = entry
	}

	// Shell task: command and prompt both present; runtime/model/effort absent.
	shell := byID["shell-echo"]
	if shell == nil {
		t.Fatalf("shell-echo task missing from JSON")
	}
	if got := shell["command"]; got != "echo {{msg}}" {
		t.Errorf("shell-echo command = %v, want %q", got, "echo {{msg}}")
	}
	if got := shell["prompt"]; got != "echo hello world" {
		t.Errorf("shell-echo prompt = %v, want %q", got, "echo hello world")
	}
	if got := shell["output"]; got != "hello world\n" {
		t.Errorf("shell-echo output = %v, want %q", got, "hello world\n")
	}
	for _, key := range []string{"runtime", "model", "effort"} {
		if v, present := shell[key]; present && v != "" {
			t.Errorf("shell-echo %s = %v, want absent/empty", key, v)
		}
	}

	// LLM task: prompt round-trips; command absent.
	llm := byID["llm-summarise"]
	if llm == nil {
		t.Fatalf("llm-summarise task missing from JSON")
	}
	if got := llm["prompt"]; got != "summarise this" {
		t.Errorf("llm-summarise prompt = %v, want %q", got, "summarise this")
	}
	if v, present := llm["command"]; present && v != "" {
		t.Errorf("llm-summarise command = %v, want absent", v)
	}
	if got := llm["runtime"]; got != "claude-code" {
		t.Errorf("llm-summarise runtime = %v, want claude-code", got)
	}
}

// TestRunIDIsUniqueAndSortable pins the two properties run ids exist to
// provide: uniqueness under rapid creation (random suffix) and lexical
// sortability (timestamp prefix). Without these, latest.json and an `ls`
// listing of run files would be ambiguous.
func TestRunIDIsUniqueAndSortable(t *testing.T) {
	root := t.TempDir()
	pattern := regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{6}$`)

	seen := map[string]bool{}
	for i := range 16 {
		r, err := store.Open("wf", []byte("name: wf\n"), store.Config{Root: root})
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		id := r.ID()
		if !pattern.MatchString(id) {
			t.Fatalf("id %q does not match %s", id, pattern)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q on iteration %d", id, i)
		}
		seen[id] = true
	}
}
