package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestLoadStateAbsentReturnsEmptyMap pins the first-tick contract: a workflow
// with no state file on disk loads as a fresh, non-nil, empty map rather than
// an error, so callers can write into it directly.
func TestLoadStateAbsentReturnsEmptyMap(t *testing.T) {
	root := t.TempDir()
	got, err := store.LoadState(root, workflow.WorkflowID("wf"))
	if err != nil {
		t.Fatalf("LoadState on absent file: %v", err)
	}
	if got == nil {
		t.Fatal("LoadState returned nil map, want empty non-nil map")
	}
	if len(got) != 0 {
		t.Fatalf("LoadState returned %v, want empty map", got)
	}
}

// TestSaveStateThenLoadStateRoundTrips writes a state map and reads it back,
// asserting the values survive the JSON round trip unchanged.
func TestSaveStateThenLoadStateRoundTrips(t *testing.T) {
	root := t.TempDir()
	wf := workflow.WorkflowID("triage")
	want := map[string]string{
		"done":   "line1\nline2",
		"cursor": "42",
	}
	if err := store.SaveState(root, wf, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := store.LoadState(root, wf)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("LoadState = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("state[%q] = %q, want %q", k, got[k], v)
		}
	}
}

// TestSaveStateAtomicLeavesNoTempFile asserts the write-then-rename idiom
// leaves only the canonical .json file behind, never a stray .tmp sibling.
func TestSaveStateAtomicLeavesNoTempFile(t *testing.T) {
	root := t.TempDir()
	wf := workflow.WorkflowID("wf")
	if err := store.SaveState(root, wf, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	dir := filepath.Join(root, "state")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("found leftover temp file %q", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "wf.json")); err != nil {
		t.Errorf("canonical state file missing: %v", err)
	}
}

// TestSaveStateDefaultsRootToLoom pins that an empty root resolves to ".loom",
// matching Config.Root's default, so callers can omit it.
func TestSaveStateDefaultsRootToLoom(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	wf := workflow.WorkflowID("wf")
	if err := store.SaveState("", wf, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".loom", "state", "wf.json")); err != nil {
		t.Errorf("expected state under .loom: %v", err)
	}
	got, err := store.LoadState("", wf)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got["k"] != "v" {
		t.Errorf("state[k] = %q, want v", got["k"])
	}
}
