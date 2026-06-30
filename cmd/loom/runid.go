package main

import "github.com/mcuste/loom/pkg/store"

// loadRunRecord resolves a user-supplied run id to its record path and loads
// it. It is the shared prelude behind `loom resume` and `loom runs show`,
// both of which turn an id into a record before acting on it. The resolution
// logic (fuzzy matching, latest symlink, traversal guard) lives in store so
// the on-disk run layout is owned by one package.
func loadRunRecord(home, runID string) (*store.RunRecord, error) {
	return store.LoadByRunID(home, runID)
}
