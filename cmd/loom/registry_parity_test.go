package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRegistryResolveWalkParity pins the two registry resolution paths together:
// for every workflow walkRegistry enumerates (the source for `workflows ls` and
// completion), resolveWorkflowRef (the source for `loom run <name>`) must map the
// same colon-name to the very file walkRegistry reported. The precedence rules
// (flat-over-dir, .yaml-over-.yml, eponymous-dir collapse) live in shared helpers
// now; this guards against the two callers drifting if those rules change.
func TestRegistryResolveWalkParity(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir()) // a non-git temp dir: no local .loom/workflows interferes
	root := filepath.Join(home, "workflows")

	// A tree exercising every collision the precedence rules arbitrate.
	for _, rel := range []string{
		"flat.yaml",            // plain flat file
		"flat/flat.yaml",       // dir form colliding with flat.yaml -> flat wins
		"dironly/dironly.yaml", // eponymous-dir form, no flat sibling -> name "dironly"
		"both.yaml",            // .yaml vs .yml collision -> .yaml wins
		"both.yml",
		"ymlonly.yml",      // .yml-only -> name "ymlonly"
		"nested/deep.yaml", // colon-name "nested:deep"
	} {
		writeRegistryFile(t, root, rel)
	}

	refs, err := listRegistryWorkflows()
	if err != nil {
		t.Fatalf("listRegistryWorkflows: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected the seeded registry to enumerate at least one workflow")
	}
	for _, r := range refs {
		got, err := resolveWorkflowRef(r.name)
		if err != nil {
			t.Errorf("resolveWorkflowRef(%q): %v", r.name, err)
			continue
		}
		if got != r.path {
			t.Errorf("name %q: walkRegistry -> %q but resolveWorkflowRef -> %q", r.name, r.path, got)
		}
	}
}

// writeRegistryFile creates root/rel (with parents) holding a minimal valid
// workflow, so both resolution paths see a real file on disk.
func writeRegistryFile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte("name: x\ntasks:\n  - id: a\n    command: echo hi\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
