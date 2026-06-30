package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// loomHome resolves loom's global on-disk data directory: $LOOM_HOME when set,
// otherwise $HOME/.loom (via os.UserHomeDir). It creates the directory (and
// parents) and returns its resolved absolute path. A clear error is returned
// when neither LOOM_HOME nor a user home directory can be resolved.
func loomHome() (string, error) {
	dir := os.Getenv("LOOM_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve loom home: %w", err)
		}
		dir = filepath.Join(home, ".loom")
	}
	// Resolve to an absolute path before creating it: a relative LOOM_HOME would
	// otherwise resolve against the current dir, so the two loomHome calls that
	// straddle a chdir (run, then resume) would split the store across two
	// on-disk locations.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve loom home %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create loom home %s: %w", dir, err)
	}
	return dir, nil
}

// scheduleDaemonLog returns the daemon's stdout/stderr log path:
// <home>/schedules/daemon.log.
func scheduleDaemonLog(home string) string {
	return filepath.Join(home, "schedules", "daemon.log")
}

// workflowsDir returns the global workflow registry directory:
// <home>/workflows.
func workflowsDir(home string) string {
	return filepath.Join(home, "workflows")
}
