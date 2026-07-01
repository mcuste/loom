package store

import (
	"path/filepath"

	"github.com/mcuste/loom/pkg/workflow"
)

// Home is loom's on-disk data directory. The zero value and the empty string
// both map to ".loom" via the constructor, so callers that omit a root get the
// conventional default. All on-disk layout knowledge lives here: files in
// list.go, runid.go, state.go, and store.go use these methods instead of
// re-encoding filepath.Join(root, "runs", ...) independently.
type Home string

// NewHome converts a string root into a Home, applying the ".loom" default
// when root is empty. It is the single authority for the rootOrDefault idiom.
func NewHome(root string) Home {
	if root == "" {
		return Home(".loom")
	}
	return Home(root)
}

// runsDir returns the directory that holds per-workflow run subdirectories.
func (h Home) runsDir() string {
	return filepath.Join(string(h), "runs")
}

// workflowRunsDir returns the directory for a specific workflow's run records.
func (h Home) workflowRunsDir(wfID string) string {
	return filepath.Join(string(h), "runs", wfID)
}

// runPath returns the on-disk path for a single run record.
func (h Home) runPath(wfID, runID string) string {
	return filepath.Join(string(h), "runs", wfID, runID+".json")
}

// latestPath returns the symlink path for the latest run of a workflow.
func (h Home) latestPath(wfID string) string {
	return filepath.Join(string(h), "runs", wfID, "latest.json")
}

// statePath returns the cross-run state file path for a workflow.
func (h Home) statePath(wf workflow.WorkflowID) string {
	return filepath.Join(string(h), "state", string(wf)+".json")
}

// stateDir returns the directory that holds cross-run state files.
func (h Home) stateDir() string {
	return filepath.Join(string(h), "state")
}
