package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mcuste/loom/pkg/store"
)

// writeRunFile drops a minimal run record JSON under
// <root>/runs/<wf>/<id>.json so the listing helpers have something to decode.
func writeRunFile(t *testing.T, root, wf, id, body string) {
	t.Helper()
	dir := filepath.Join(root, "runs", wf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write run file: %v", err)
	}
}

// TestListRunsNewestFirst asserts that ListRuns decodes the header fields and
// orders the records by start time descending.
func TestListRunsNewestFirst(t *testing.T) {
	root := t.TempDir()
	writeRunFile(t, root, "deploy", "20260101T000000Z-aaaaaa", `{
		"run_id": "20260101T000000Z-aaaaaa",
		"workflow_id": "deploy",
		"started_at": "2026-01-01T00:00:00Z",
		"finished_at": "2026-01-01T00:01:00Z",
		"elapsed_ms": 60000,
		"status": "ok",
		"task_count": 3,
		"usage": {"total_cost_usd": 0.05}
	}`)
	writeRunFile(t, root, "deploy", "20260102T000000Z-bbbbbb", `{
		"run_id": "20260102T000000Z-bbbbbb",
		"workflow_id": "deploy",
		"started_at": "2026-01-02T00:00:00Z",
		"status": "failed",
		"error": "build broke"
	}`)

	got, err := store.ListRuns(root, "deploy")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 runs, got %d", len(got))
	}
	if got[0].RunID != "20260102T000000Z-bbbbbb" {
		t.Errorf("newest first: got %q", got[0].RunID)
	}
	if got[0].Status != "failed" || got[0].Error != "build broke" {
		t.Errorf("header status/error not decoded: %+v", got[0])
	}
	if got[1].TotalCostUSD != 0.05 || got[1].TaskCount != 3 {
		t.Errorf("usage/task_count not decoded: %+v", got[1])
	}
	if got[1].Path == "" {
		t.Errorf("Path should be set for Load")
	}
}

// TestListRunsSkipsLatestSymlink ensures the latest.json convenience link is
// not counted as a separate run.
func TestListRunsSkipsLatestSymlink(t *testing.T) {
	root := t.TempDir()
	writeRunFile(t, root, "deploy", "20260101T000000Z-aaaaaa", `{
		"run_id": "20260101T000000Z-aaaaaa",
		"workflow_id": "deploy",
		"started_at": "2026-01-01T00:00:00Z",
		"status": "ok"
	}`)
	link := filepath.Join(root, "runs", "deploy", "latest.json")
	if err := os.Symlink("20260101T000000Z-aaaaaa.json", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := store.ListRuns(root, "deploy")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("latest.json should be skipped, got %d runs", len(got))
	}
}

// TestListAllRunsMergesWorkflows asserts the cross-workflow index merges and
// re-sorts records from every workflow directory.
func TestListAllRunsMergesWorkflows(t *testing.T) {
	root := t.TempDir()
	writeRunFile(t, root, "deploy", "20260101T000000Z-aaaaaa", `{
		"run_id": "20260101T000000Z-aaaaaa", "workflow_id": "deploy",
		"started_at": "2026-01-01T00:00:00Z", "status": "ok"
	}`)
	writeRunFile(t, root, "nightly", "20260103T000000Z-cccccc", `{
		"run_id": "20260103T000000Z-cccccc", "workflow_id": "nightly",
		"started_at": "2026-01-03T00:00:00Z", "status": "ok"
	}`)

	got, err := store.ListAllRuns(root)
	if err != nil {
		t.Fatalf("ListAllRuns: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 runs across workflows, got %d", len(got))
	}
	if got[0].WorkflowID != "nightly" {
		t.Errorf("newest across workflows first: got %q", got[0].WorkflowID)
	}

	wfs, err := store.ListWorkflows(root)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(wfs) != 2 || wfs[0] != "deploy" || wfs[1] != "nightly" {
		t.Errorf("ListWorkflows sorted: got %v", wfs)
	}
}

// TestListRunsMissingDirIsEmpty confirms a fresh install (no runs yet) lists
// cleanly rather than erroring.
func TestListRunsMissingDirIsEmpty(t *testing.T) {
	root := t.TempDir()
	got, err := store.ListAllRuns(root)
	if err != nil {
		t.Fatalf("ListAllRuns on empty root: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 runs, got %d", len(got))
	}
}
