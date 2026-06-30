package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ResolveRunID resolves a user-supplied run id to a path under <root>/runs,
// across every workflow directory. The literal "latest" follows the
// most-recently-updated latest.json symlink. Any other value matches a run
// when it equals the full id, the short suffix shown in the runs table (the
// hex after the last "-", e.g. "0afad3"), or a leading timestamp prefix.
// An exact full-id match always wins; otherwise a single fuzzy match is
// returned and multiple are reported as ambiguous. The runID must be a single
// path component (no separators) so a crafted value cannot escape the runs
// root via `..` traversal. Shared by `loom resume` and `loom runs show`.
func ResolveRunID(root, runID string) (string, error) {
	runsRoot := filepath.Join(rootOrDefault(root), "runs")
	if runID == "latest" {
		return findLatestRecord(runsRoot)
	}
	if err := validateRunID(runID); err != nil {
		return "", err
	}
	headers, err := ListAllRuns(root)
	if err != nil {
		return "", err
	}
	var fuzzy []RunHeader
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
		return "", fmt.Errorf("run id %q: not found under %s", runID, runsRoot)
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

// LoadByRunID resolves a user-supplied run id and loads its record. It is the
// deep entry point that hides the on-disk run layout (directory walk, latest
// symlink, fuzzy matching, traversal guard) from callers: they supply a home
// root and a user-supplied id; the store takes care of the rest.
func LoadByRunID(root, runID string) (*RunRecord, error) {
	path, err := ResolveRunID(root, runID)
	if err != nil {
		return nil, err
	}
	return Load(path)
}

// WorkflowLatestPath returns the on-disk path of the latest-run symlink for a
// specific workflow: <root>/runs/<workflowID>/latest.json. It is the single
// authority for this path so callers that need "the last run of workflow X" do
// not re-derive the layout independently.
func WorkflowLatestPath(root, workflowID string) string {
	return filepath.Join(rootOrDefault(root), "runs", workflowID, "latest.json")
}

// validateRunID rejects ids that contain a path separator; without this,
// filepath.Join silently cleans `../../foo` to a path outside the runs root
// and Load reads an arbitrary file off disk. The `..` traversal vectors
// (`../`, `..\`) all carry a separator, so the separator check covers them;
// a bare `..` substring (e.g. `a..b`) is a legitimate id and must not be
// rejected.
func validateRunID(runID string) error {
	if runID == "" {
		return errors.New("run id: empty")
	}
	if strings.ContainsAny(runID, `/\`) {
		return fmt.Errorf("run id %q: must be a single path component", runID)
	}
	return nil
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

// findLatestRecord picks the most-recently-modified <runsRoot>/*/latest.json
// link, so "latest" resolves to the user's most recent run even when several
// workflows share the home directory.
func findLatestRecord(runsRoot string) (string, error) {
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", runsRoot, err)
	}
	var (
		best     string
		bestTime time.Time
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		link := filepath.Join(runsRoot, e.Name(), "latest.json")
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
		return "", fmt.Errorf("no latest run found under %s", runsRoot)
	}
	return best, nil
}
