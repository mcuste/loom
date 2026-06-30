package main

import "path/filepath"

// resolveSubWorkflowRef maps a task's `workflow:` ref to a file path.
// resolveWorkflowRef returns path-mode args verbatim, so when the returned
// value equals ref the arg was a filesystem path that needs anchoring to
// parentDir (unless it is already absolute).
func resolveSubWorkflowRef(ref, parentDir string) (string, error) {
	resolved, err := resolveWorkflowRef(ref)
	if err != nil {
		return "", err
	}
	// Path-mode: resolveWorkflowRef returned the arg unchanged. Anchor
	// relative paths against the parent workflow's directory.
	if resolved == ref && !filepath.IsAbs(resolved) {
		return filepath.Join(parentDir, resolved), nil
	}
	return resolved, nil
}
