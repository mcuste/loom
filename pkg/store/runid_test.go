package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/store"
)

// writeRunRecord drops a synthetic <root>/runs/<wfID>/<runID>.json file.
func writeRunRecord(t *testing.T, root, wfID, runID, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, "runs", wfID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	rec := map[string]any{
		"run_id":      runID,
		"workflow_id": wfID,
		"started_at":  "2026-06-09T14:30:52Z",
		"status":      "failed",
		"manifest":    manifest,
		"tasks":       []any{},
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

// linkLatest creates the <root>/runs/<wfID>/latest.json symlink pointing at
// runID.json. Required for tests that resolve "latest".
func linkLatest(t *testing.T, root, wfID, runID string) {
	t.Helper()
	dir := filepath.Join(root, "runs", wfID)
	link := filepath.Join(dir, "latest.json")
	_ = os.Remove(link)
	if err := os.Symlink(runID+".json", link); err != nil {
		t.Fatalf("symlink latest: %v", err)
	}
}

// TestResolveRunID_ResolvesShortSuffixAndPrefix pins that the run-id lookup
// accepts the full id, the short hex suffix shown in the runs table, and a
// leading timestamp prefix, so users can paste what the UI displays.
func TestResolveRunID_ResolvesShortSuffixAndPrefix(t *testing.T) {
	root := t.TempDir()
	writeRunRecord(t, root, "deploy", "20260623T100000Z-0afad3", "name: deploy")

	for _, q := range []string{"20260623T100000Z-0afad3", "0afad3", "20260623"} {
		p, err := store.ResolveRunID(root, q)
		if err != nil {
			t.Fatalf("ResolveRunID(%q): %v", q, err)
		}
		if filepath.Base(p) != "20260623T100000Z-0afad3.json" {
			t.Errorf("query %q resolved to %s", q, p)
		}
	}
	if _, err := store.ResolveRunID(root, "nope"); err == nil {
		t.Errorf("expected not-found error for an unmatched id")
	}
}

// TestResolveRunID_AmbiguousSuffixErrors pins that a fragment matching more
// than one run is reported rather than silently picking one.
func TestResolveRunID_AmbiguousSuffixErrors(t *testing.T) {
	root := t.TempDir()
	writeRunRecord(t, root, "a", "20260101T000000Z-aaaaaa", "name: a")
	writeRunRecord(t, root, "b", "20260102T000000Z-aaaaaa", "name: b")

	_, err := store.ResolveRunID(root, "aaaaaa")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous error, got %v", err)
	}
}

// TestResolveRunID_LatestPicksMostRecentAcrossWorkflows pins that "latest"
// scans every workflow dir under the shared home and follows the
// most-recently modified latest.json, not just the first workflow's. The
// newer record must win regardless of the dir iteration order.
func TestResolveRunID_LatestPicksMostRecentAcrossWorkflows(t *testing.T) {
	root := t.TempDir()

	oldPath := writeRunRecord(t, root, "alpha", "20260101T000000Z-aaaaaa", "name: alpha")
	linkLatest(t, root, "alpha", "20260101T000000Z-aaaaaa")
	newPath := writeRunRecord(t, root, "beta", "20260102T000000Z-bbbbbb", "name: beta")
	linkLatest(t, root, "beta", "20260102T000000Z-bbbbbb")

	// os.Stat follows latest.json to its target record, so pin the targets'
	// mtimes to make beta unambiguously the most recent.
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(oldPath, older, older); err != nil {
		t.Fatalf("chtimes alpha: %v", err)
	}
	if err := os.Chtimes(newPath, newer, newer); err != nil {
		t.Fatalf("chtimes beta: %v", err)
	}

	p, err := store.ResolveRunID(root, "latest")
	if err != nil {
		t.Fatalf("ResolveRunID(latest): %v", err)
	}
	if got := filepath.Base(filepath.Dir(p)); got != "beta" {
		t.Errorf("latest resolved to workflow %q, want beta (the most-recent record)", got)
	}
}

// TestResolveRunID_TraversalRejected pins the security guard: a run id
// containing a path separator is rejected so a crafted value cannot escape
// the runs root via `..` traversal.
func TestResolveRunID_TraversalRejected(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"../foo", "a/b", `a\b`} {
		if _, err := store.ResolveRunID(root, bad); err == nil {
			t.Errorf("ResolveRunID(%q): want error, got nil", bad)
		}
	}
}

// TestLoadByRunID_LoadsRecord pins that LoadByRunID resolves and loads the
// record in one call.
func TestLoadByRunID_LoadsRecord(t *testing.T) {
	root := t.TempDir()
	writeRunRecord(t, root, "wf", "20260101T000000Z-aaaaaa", "name: wf")

	rec, err := store.LoadByRunID(root, "20260101T000000Z-aaaaaa")
	if err != nil {
		t.Fatalf("LoadByRunID: %v", err)
	}
	if rec.RunID != "20260101T000000Z-aaaaaa" {
		t.Errorf("RunID = %q, want 20260101T000000Z-aaaaaa", rec.RunID)
	}
}

// TestWorkflowLatestPath pins the path shape so a rename of "latest.json" or
// the runs subtree is a compile-time signal, not a silent breakage.
func TestWorkflowLatestPath(t *testing.T) {
	root := "/data/loom"
	got := store.WorkflowLatestPath(root, "mywf")
	want := filepath.Join(root, "runs", "mywf", "latest.json")
	if got != want {
		t.Errorf("WorkflowLatestPath = %q, want %q", got, want)
	}
}
