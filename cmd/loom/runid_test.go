package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFindRunRecord_ResolvesShortSuffixAndPrefix pins that the run-id lookup
// accepts the full id, the short hex suffix shown in the runs table, and a
// leading timestamp prefix, so users can paste what the UI displays.
func TestFindRunRecord_ResolvesShortSuffixAndPrefix(t *testing.T) {
	home := loomHomeForTest(t)
	writeRunRecord(t, "deploy", "20260623T100000Z-0afad3", "name: deploy", nil, nil)

	for _, q := range []string{"20260623T100000Z-0afad3", "0afad3", "20260623"} {
		p, err := findRunRecord(home, q)
		if err != nil {
			t.Fatalf("findRunRecord(%q): %v", q, err)
		}
		if filepath.Base(p) != "20260623T100000Z-0afad3.json" {
			t.Errorf("query %q resolved to %s", q, p)
		}
	}
	if _, err := findRunRecord(home, "nope"); err == nil {
		t.Errorf("expected not-found error for an unmatched id")
	}
}

// TestFindRunRecord_AmbiguousSuffixErrors pins that a fragment matching more
// than one run is reported rather than silently picking one.
func TestFindRunRecord_AmbiguousSuffixErrors(t *testing.T) {
	home := loomHomeForTest(t)
	writeRunRecord(t, "a", "20260101T000000Z-aaaaaa", "name: a", nil, nil)
	writeRunRecord(t, "b", "20260102T000000Z-aaaaaa", "name: b", nil, nil)

	_, err := findRunRecord(home, "aaaaaa")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous error, got %v", err)
	}
}

// TestFindRunRecord_LatestPicksMostRecentAcrossWorkflows pins that "latest"
// scans every workflow dir under the shared home and follows the most-recently
// modified latest.json, not just the first workflow's. The newer record must
// win regardless of the dir iteration order.
func TestFindRunRecord_LatestPicksMostRecentAcrossWorkflows(t *testing.T) {
	home := loomHomeForTest(t)

	oldPath := writeRunRecord(t, "alpha", "20260101T000000Z-aaaaaa", "name: alpha", nil, nil)
	linkLatest(t, "alpha", "20260101T000000Z-aaaaaa")
	newPath := writeRunRecord(t, "beta", "20260102T000000Z-bbbbbb", "name: beta", nil, nil)
	linkLatest(t, "beta", "20260102T000000Z-bbbbbb")

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

	p, err := findRunRecord(home, "latest")
	if err != nil {
		t.Fatalf("findRunRecord(latest): %v", err)
	}
	if got := filepath.Base(filepath.Dir(p)); got != "beta" {
		t.Errorf("latest resolved to workflow %q, want beta (the most-recent record)", got)
	}
}
