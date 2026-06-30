package registry_test

import (
	"os"
	"path/filepath"
	"testing"
)

// registryBody is a minimal parseable workflow body used to populate registry
// roots in tests. Resolution does not read the body, but `loom workflows ls`
// parses it best-effort for the description.
const registryBody = `name: x
runtime: cmd-echo
model: m1
tasks:
  - id: a
    command: echo hi
`

// writeWorkflowFile creates root/rel (with parents) holding a minimal valid
// workflow and returns the absolute path.
func writeWorkflowFile(t *testing.T, root, rel string) string {
	t.Helper()
	return writeWorkflowFileWithBody(t, root, rel, registryBody)
}

// writeWorkflowFileWithBody creates root/rel (with parents) holding body and
// returns the absolute path.
func writeWorkflowFileWithBody(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

// writeLocalWorkflowFile creates dir/.loom/workflows/rel (with parents) and
// returns the absolute path.
func writeLocalWorkflowFile(t *testing.T, dir, rel string) string {
	t.Helper()
	return writeWorkflowFile(t, filepath.Join(dir, ".loom", "workflows"), rel)
}

// mkGitRoot marks dir as a git repo root by creating a .git directory.
func mkGitRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
}

// projectTree returns a fresh temp dir with its symlinks resolved, so that
// the path it returns matches the one registry.LocalDirs derives (macOS routes
// t.TempDir() through /var -> /private/var).
func projectTree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	return root
}
