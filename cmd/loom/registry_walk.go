package main

import "github.com/mcuste/loom/pkg/registry"

// workflowRef is a single registry workflow: its colon-joined name and the
// file it resolves to. The cmd/loom layer keeps this local type so the rest of
// the package does not need to import registry.Ref directly; it has the same
// shape.
type workflowRef struct {
	name string
	path string
}

// listRegistryWorkflows resolves the ordered registry roots and walks them into
// the merged local+global workflow set. It is the shared prelude behind
// completion, `workflows ls`, and `schedule sync`.
func listRegistryWorkflows() ([]workflowRef, error) {
	roots, err := registrySearchRoots()
	if err != nil {
		return nil, err
	}
	refs, err := registry.List(roots)
	if err != nil {
		return nil, err
	}
	result := make([]workflowRef, len(refs))
	for i, r := range refs {
		result[i] = workflowRef{name: r.Name, path: r.Path}
	}
	return result, nil
}
