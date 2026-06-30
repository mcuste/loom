package registry

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Ref is a single registry workflow: its colon-joined name and the file it
// resolves to.
type Ref struct {
	Name string
	Path string
}

// List returns all workflows reachable under roots (in order), merged with
// nearest-root shadowing: a name present in an earlier root is not overridden
// by a later one. The result is sorted by name.
func List(roots []string) ([]Ref, error) {
	return walkRegistries(roots)
}

// registryName converts a file path relative to a registry root (carrying the
// extension ext) into its colon-joined registry name. The final path segment
// is dropped when it equals its immediate parent directory (the eponymous-dir
// form `X/X.yaml` -> `X`), so a workflow that lives in its own directory
// beside `prompt_file:` text it references does not pick up a redundant
// trailing segment. It reports whether that collapse happened so a caller can
// give a flat file precedence over a colliding dir form.
func registryName(rel, ext string) (name string, collapsed bool) {
	parts := strings.Split(strings.TrimSuffix(rel, ext), string(filepath.Separator))
	if n := len(parts); n >= 2 && parts[n-1] == parts[n-2] {
		parts = parts[:n-1]
		collapsed = true
	}
	return strings.Join(parts, ":"), collapsed
}

// walkRegistry walks root, returning every *.yaml/*.yml file as a Ref whose
// Name is the path relative to root with '/'->':' and the extension stripped,
// collapsing the eponymous-dir form `X/X.yaml` to `X` (see registryName),
// sorted by name. When two files collide on the same name, the one with higher
// precedence wins: flat form over dir form (via precedence(collapsed)), and
// .yaml over .yml (WalkDir visits lexically, so the first file for a given
// name key is kept). An absent registry root yields no refs.
func walkRegistry(root string) ([]Ref, error) {
	var refs []Ref
	// idx maps a registry name to its slot in refs; collapsedMap records
	// whether that slot came from an eponymous-dir path, so a later flat file
	// can shadow it using precedence().
	idx := make(map[string]int)
	collapsedMap := make(map[string]bool)
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
		if !IsWorkflowExt(ext) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name, isCollapsed := registryName(rel, ext)
		if i, ok := idx[name]; ok {
			// A flat file shadows a previously recorded dir form; any other
			// collision (.yaml already chosen over .yml, same form) keeps the
			// first. precedence(existing) > precedence(new) means the new entry
			// wins.
			if precedence(collapsedMap[name]) > precedence(isCollapsed) {
				refs[i] = Ref{Name: name, Path: path}
				collapsedMap[name] = isCollapsed
			}
			return nil
		}
		idx[name] = len(refs)
		collapsedMap[name] = isCollapsed
		refs = append(refs, Ref{Name: name, Path: path})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sortRefs(refs)
	return refs, nil
}

// sortRefs orders refs by colon-name in place.
func sortRefs(refs []Ref) {
	slices.SortFunc(refs, func(a, b Ref) int { return strings.Compare(a.Name, b.Name) })
}

// walkRegistries walks each root in order via walkRegistry, merging results
// into one set keyed by colon-name where the first occurrence wins (nearest
// root shadows the rest), then re-sorts by name.
func walkRegistries(roots []string) ([]Ref, error) {
	var refs []Ref
	seen := make(map[string]bool)
	for _, root := range roots {
		rs, err := walkRegistry(root)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if seen[r.Name] {
				continue
			}
			seen[r.Name] = true
			refs = append(refs, r)
		}
	}
	sortRefs(refs)
	return refs, nil
}
