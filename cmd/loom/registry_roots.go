package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// registrySearchRoots returns the ordered list of registry roots searched for a
// name: the project-local `.loom/workflows` dirs walking up from the current
// working directory to the git root, then the global $LOOM_HOME/workflows last.
func registrySearchRoots() ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	roots := localRegistryDirs(cwd)
	home, err := loomHome()
	if err != nil {
		return nil, err
	}
	return append(roots, filepath.Join(home, "workflows")), nil
}

// localRegistryDirs returns the `.loom/workflows` dirs from start walking up to
// and including the git repo root (the first ancestor containing a `.git`). If
// no git root is found up to the filesystem root, only start's local dir is
// returned, so resolution never scans the whole filesystem.
func localRegistryDirs(start string) []string {
	var dirs []string
	dir := start
	for {
		dirs = append(dirs, filepath.Join(dir, ".loom", "workflows"))
		if isGitRoot(dir) {
			return dirs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root with no git root: only the start
			// dir's local registry is searched.
			return dirs[:1]
		}
		dir = parent
	}
}

// isGitRoot reports whether dir contains a `.git` entry, marking it a git repo
// root. Both a directory (normal repo) and a file (worktree or submodule) count.
func isGitRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
