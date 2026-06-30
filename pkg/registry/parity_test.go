package registry_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/registry"
)

// TestResolveListParity pins the two registry resolution paths together: for
// every workflow List enumerates (the source for `workflows ls` and
// completion), Resolve must map the same colon-name to the very file List
// reported. The precedence rules (flat-over-dir, .yaml-over-.yml, eponymous-dir
// collapse) are encoded in the shared precedence() helper; this test guards
// against the two callers drifting if those rules change.
func TestResolveListParity(t *testing.T) {
	root := t.TempDir()

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
		writeWorkflowFileWithBody(t, root, rel, registryBody)
	}

	refs, err := registry.List([]string{root})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected the seeded registry to enumerate at least one workflow")
	}
	for _, r := range refs {
		got, err := registry.Resolve([]string{root}, r.Name)
		if err != nil {
			t.Errorf("Resolve(%q): %v", r.Name, err)
			continue
		}
		if got != r.Path {
			t.Errorf("name %q: List -> %q but Resolve -> %q", r.Name, r.Path, got)
		}
	}
}
