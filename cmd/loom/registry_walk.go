package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// workflowRef is a single registry workflow: its colon-joined name and the
// file it resolves to.
type workflowRef struct {
	name string
	path string
}

// registryName converts a file path relative to a registry root (carrying the
// extension ext) into its colon-joined registry name. The final path segment is
// dropped when it equals its immediate parent directory (the eponymous-dir form
// `X/X.yaml` -> `X`), so a workflow that lives in its own directory beside the
// `prompt_file:` text it references does not pick up a redundant trailing
// segment. It reports whether that collapse happened, so a caller can give a
// flat file precedence over a colliding dir form.
func registryName(rel, ext string) (name string, collapsed bool) {
	parts := strings.Split(strings.TrimSuffix(rel, ext), string(filepath.Separator))
	if n := len(parts); n >= 2 && parts[n-1] == parts[n-2] {
		parts = parts[:n-1]
		collapsed = true
	}
	return strings.Join(parts, ":"), collapsed
}

// walkRegistry walks root, returning every *.yaml/*.yml file as a workflowRef
// whose name is the path relative to root with '/'->':' and the extension
// stripped, collapsing the eponymous-dir form `X/X.yaml` to `X` (see
// registryName), sorted by name. A <stem>.yaml and <stem>.yml collide on one
// name; WalkDir visits lexically (.yaml before .yml), so the first wins and the
// rest are dropped, mirroring the '.yaml'-over-'.yml' preference in
// resolveWorkflowRef. A flat file `X.yaml` and a dir form `X/X.yaml` also
// collide on the name `X`; the flat file wins (it shadows the dir form),
// matching resolveWorkflowRef. An absent registry root yields no refs.
func walkRegistry(root string) ([]workflowRef, error) {
	var refs []workflowRef
	// idx maps a registry name to its slot in refs; collapsed records whether
	// that slot came from an eponymous-dir path, so a later flat file can
	// shadow it (flat wins).
	idx := make(map[string]int)
	collapsed := make(map[string]bool)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // empty registry: nothing to list
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if !isWorkflowExt(ext) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name, isCollapsed := registryName(rel, ext)
		if i, ok := idx[name]; ok {
			// Flat file shadows a previously recorded dir form; any other
			// collision (same form, or .yaml already chosen over .yml) keeps
			// the first.
			if collapsed[name] && !isCollapsed {
				refs[i] = workflowRef{name: name, path: path}
				collapsed[name] = false
			}
			return nil
		}
		idx[name] = len(refs)
		collapsed[name] = isCollapsed
		refs = append(refs, workflowRef{name: name, path: path})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sortRefsByName(refs)
	return refs, nil
}

// sortRefsByName orders refs by colon-name in place. Both walkRegistry and
// walkRegistries sort by the same key; centralizing it keeps the two ordering
// rules from drifting.
func sortRefsByName(refs []workflowRef) {
	slices.SortFunc(refs, func(a, b workflowRef) int { return strings.Compare(a.name, b.name) })
}

// walkRegistries walks each root in order via walkRegistry, merging the results
// into one set keyed by colon-name where the first occurrence wins (nearest root
// shadows the rest), then re-sorts by name. It lets `workflows ls` and
// completion present the effective local+global registry.
func walkRegistries(roots []string) ([]workflowRef, error) {
	var refs []workflowRef
	seen := make(map[string]bool)
	for _, root := range roots {
		rs, err := walkRegistry(root)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if seen[r.name] {
				continue
			}
			seen[r.name] = true
			refs = append(refs, r)
		}
	}
	sortRefsByName(refs)
	return refs, nil
}

// listRegistryWorkflows resolves the ordered registry roots and walks them into
// the merged local+global workflow set. It is the shared prelude behind
// completion, `workflows ls`, and `schedule sync`.
func listRegistryWorkflows() ([]workflowRef, error) {
	roots, err := registrySearchRoots()
	if err != nil {
		return nil, err
	}
	return walkRegistries(roots)
}
