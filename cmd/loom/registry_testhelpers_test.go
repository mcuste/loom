package main

import (
	"os"
	"path/filepath"
	"testing"
)

// registryMinimalBody is a minimal parseable workflow body used to populate
// registry roots in tests. Resolution does not read the body, but
// `loom workflows ls` parses it best-effort for descriptions.
const registryMinimalBody = `name: x
runtime: cmd-echo
model: m1
tasks:
  - id: a
    command: echo hi
`

// writeRegistryWF creates home/workflows/rel (with parents) holding a minimal
// valid workflow body and returns the absolute path.
func writeRegistryWF(t *testing.T, home, rel string) string {
	t.Helper()
	return writeRegistryWorkflow(t, home, rel, registryMinimalBody)
}

// writeRegistryWorkflow creates home/workflows/rel (with parents) holding body
// and returns the absolute path.
func writeRegistryWorkflow(t *testing.T, home, rel, body string) string {
	t.Helper()
	path := filepath.Join(home, "workflows", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

// writeLocalRegistryWF creates root/.loom/workflows/rel (with parents) holding
// a minimal valid workflow body and returns the absolute path.
func writeLocalRegistryWF(t *testing.T, root, rel string) string {
	t.Helper()
	return writeLocalRegistryWorkflow(t, root, rel, registryMinimalBody)
}

// writeLocalRegistryWorkflow creates root/.loom/workflows/rel (with parents)
// holding body and returns the absolute path.
func writeLocalRegistryWorkflow(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, ".loom", "workflows", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

// projectTree returns a fresh temp dir with its symlinks resolved, so that
// the path it returns matches what registry.LocalDirs derives (macOS routes
// t.TempDir() through /var -> /private/var).
func projectTree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	return root
}
