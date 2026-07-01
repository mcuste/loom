package store

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mcuste/loom/pkg/workflow"
)

// LoadState reads the cross-run state map for wf from
// <root>/state/<workflow_id>.json. A missing file is not an error: it returns
// a fresh empty map so first-tick callers can write into it directly. root
// defaults to ".loom" when empty, matching [Config.Root].
func LoadState(root string, wf workflow.WorkflowID) (map[string]string, error) {
	h := NewHome(root)
	path := h.statePath(wf)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("store: read state %s: %w", path, err)
	}
	state := map[string]string{}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("store: parse state %s: %w", path, err)
	}
	return state, nil
}

// SaveState atomically writes the cross-run state map for wf to
// <root>/state/<workflow_id>.json. The write is atomic: the bytes go to a .tmp
// sibling and are renamed into place, so a crash mid-write never leaves a
// half-written state file. root defaults to ".loom" when empty.
func SaveState(root string, wf workflow.WorkflowID, state map[string]string) error {
	h := NewHome(root)
	if err := os.MkdirAll(h.stateDir(), 0o755); err != nil {
		return fmt.Errorf("store: create state dir: %w", err)
	}
	path := h.statePath(wf)
	if err := writeJSONAtomic(path, state); err != nil {
		return fmt.Errorf("store: write state: %w", err)
	}
	return nil
}
