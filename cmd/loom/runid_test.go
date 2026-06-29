package main

import (
	"path/filepath"
	"strings"
	"testing"
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
