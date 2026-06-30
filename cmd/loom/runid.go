package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mcuste/loom/pkg/store"
)

// runIDFromPath extracts the run id from a run-record path (its basename minus
// the .json extension). Returns "" for an empty path.
func runIDFromPath(p string) string {
	if p == "" {
		return ""
	}
	base := filepath.Base(p)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// findRunRecord resolves a user-supplied run id to a path under
// $LOOM_HOME/runs, across every workflow directory. The literal "latest"
// follows the most-recently-updated latest.json symlink. Any other value
// matches a run when it equals the full id, the short suffix shown in the runs
// table (the hex after the last "-", e.g. "0afad3"), or a leading timestamp
// prefix. An exact full-id match always wins; otherwise a single fuzzy match is
// returned and multiple are reported as ambiguous. The runID must be a single
// path component (no separators) so a crafted value cannot escape the runs
// root via `..` traversal. Shared by `loom resume` and `loom runs show`.
func findRunRecord(home, runID string) (string, error) {
	root := filepath.Join(home, "runs")
	if runID == "latest" {
		return findLatestRecord(root)
	}
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	headers, err := store.ListAllRuns(home)
	if err != nil {
		return "", err
	}
	var fuzzy []store.RunHeader
	for _, h := range headers {
		if h.RunID == runID {
			return h.Path, nil // exact full-id match wins outright
		}
		if runIDMatches(h.RunID, runID) {
			fuzzy = append(fuzzy, h)
		}
	}
	switch len(fuzzy) {
	case 0:
		return "", fmt.Errorf("run id %q: not found under %s", runID, root)
	case 1:
		return fuzzy[0].Path, nil
	default:
		ids := make([]string, len(fuzzy))
		for i, h := range fuzzy {
			ids[i] = h.RunID
		}
		return "", fmt.Errorf("run id %q is ambiguous; matches %d runs: %s",
			runID, len(fuzzy), strings.Join(ids, ", "))
	}
}

// loadRunRecord resolves a user-supplied run id to its record path and loads it.
// It is the shared prelude behind `loom resume` and `loom runs show`, both of
// which turn an id into a record before acting on it.
func loadRunRecord(home, runID string) (*store.RunRecord, error) {
	path, err := findRunRecord(home, runID)
	if err != nil {
		return nil, err
	}
	return store.Load(path)
}

// runIDMatches reports whether the stored full run id matches a user-supplied
// fragment: its short suffix (the hex after the last "-") or a leading prefix
// (e.g. the timestamp). Exact equality is handled by the caller.
func runIDMatches(full, q string) bool {
	if i := strings.LastIndexByte(full, '-'); i >= 0 && full[i+1:] == q {
		return true
	}
	return strings.HasPrefix(full, q)
}

// validateRunID rejects ids that contain a path separator; without this,
// filepath.Join silently cleans `../../foo` to a path outside the runs root and
// Load reads an arbitrary file off disk. The `..` traversal vectors (`../`,
// `..\`) all carry a separator, so the separator check covers them; a bare `..`
// substring (e.g. `a..b`) is a legitimate id and must not be rejected.
func validateRunID(runID string) error {
	if runID == "" {
		return errors.New("run id: empty")
	}
	if strings.ContainsAny(runID, `/\`) {
		return fmt.Errorf("run id %q: must be a single path component", runID)
	}
	return nil
}

// findLatestRecord picks the most-recently-modified <home>/runs/*/latest.json
// link, so `loom resume latest` resolves to the user's most recent run even
// when several workflows share the home directory.
func findLatestRecord(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", root, err)
	}
	var (
		best     string
		bestTime time.Time
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		link := filepath.Join(root, e.Name(), "latest.json")
		info, err := os.Stat(link)
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			best = link
		}
	}
	if best == "" {
		return "", fmt.Errorf("no latest run found under %s", root)
	}
	return best, nil
}
