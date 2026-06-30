package main

import (
	"path/filepath"
	"testing"
)

// TestLoadRunRecord_SmokesTheSeam verifies that loadRunRecord delegates to
// store.LoadByRunID correctly: a record written to the test home resolves by
// full id and by fuzzy suffix, proving the seam from cmd/ into store is wired.
// The exhaustive resolution cases (ambiguous, latest, traversal guard) live in
// pkg/store/runid_test.go where the logic resides.
func TestLoadRunRecord_SmokesTheSeam(t *testing.T) {
	home := loomHomeForTest(t)
	writeRunRecord(t, "smoke", "20260101T000000Z-aabbcc", "name: smoke", nil, nil)

	// Full id.
	rec, err := loadRunRecord(home, "20260101T000000Z-aabbcc")
	if err != nil {
		t.Fatalf("loadRunRecord full id: %v", err)
	}
	if rec.RunID != "20260101T000000Z-aabbcc" {
		t.Errorf("RunID = %q, want 20260101T000000Z-aabbcc", rec.RunID)
	}

	// Short hex suffix.
	rec2, err := loadRunRecord(home, "aabbcc")
	if err != nil {
		t.Fatalf("loadRunRecord suffix: %v", err)
	}
	if filepath.Base(rec2.RunID) != "20260101T000000Z-aabbcc" {
		t.Errorf("suffix resolved to RunID %q, want 20260101T000000Z-aabbcc", rec2.RunID)
	}
}
