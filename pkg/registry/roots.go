package registry

import (
	"os"
	"path/filepath"
)

// LocalDirs returns the .loom/workflows directories from start walking up to
// and including the git repo root (the first ancestor containing a .git
// entry). If no git root is found up to the filesystem root, only start's
// local directory is returned, so resolution never scans the whole filesystem.
func LocalDirs(start string) []string {
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

// isGitRoot reports whether dir contains a .git entry, marking it a git repo
// root. Both a directory (normal repo) and a file (worktree or submodule)
// count.
func isGitRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
