package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsGitRoot pins that isGitRoot treats both a .git directory (normal repo)
// and a .git file (worktree/submodule) as a root, and a dir with no .git as
// not a root. The file case guards the documented worktree behavior so a
// refactor to Stat+IsDir cannot silently break git-worktree users.
func TestIsGitRoot(t *testing.T) {
	t.Run("dir is root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir .git: %v", err)
		}
		if !isGitRoot(dir) {
			t.Errorf("isGitRoot(%q with .git dir) = false, want true", dir)
		}
	})
	t.Run("file is root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatalf("write .git file: %v", err)
		}
		if !isGitRoot(dir) {
			t.Errorf("isGitRoot(%q with .git file) = false, want true", dir)
		}
	})
	t.Run("no .git is not root", func(t *testing.T) {
		dir := t.TempDir()
		if isGitRoot(dir) {
			t.Errorf("isGitRoot(%q without .git) = true, want false", dir)
		}
	})
}
