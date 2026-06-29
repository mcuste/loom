package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// resolveWorkflowRef maps a `loom run`/`check` positional argument to a workflow
// YAML path. The classification is syntactic and cwd-independent: a path
// separator always wins (so a Windows drive path like `C:\wf.yaml` is a PATH
// despite its ':'); absent a separator, arg is a registry NAME when it contains
// ':' OR does not end in .yaml/.yml; otherwise it is a filesystem PATH and is
// returned verbatim without consulting any registry. A name resolves against the
// ordered registry roots (project-local `.loom/workflows` walking up to the git
// root, then $LOOM_HOME/workflows) with ':' as the hierarchy separator: a
// trailing .yaml/.yml on the final component is stripped, then '.yaml' (fallback
// '.yml') is appended. A name also resolves to the eponymous-dir form
// `<...>/<cn>/<cn>.yaml`, so a workflow can live in its own directory beside the
// `prompt_file:` text it references; the flat file `<...>/<cn>.yaml` wins when
// both exist in one root. The first existing file along the search order wins
// (nearest shadows global). Empty, '.', and '..' components are rejected, as is
// any name that would escape a workflows root.
func resolveWorkflowRef(arg string) (string, error) {
	if !isRegistryName(arg) {
		return arg, nil
	}
	parts, err := splitWorkflowName(arg)
	if err != nil {
		return "", err
	}
	roots, err := registrySearchRoots()
	if err != nil {
		return "", err
	}

	for _, root := range roots {
		for _, stem := range candidateStems(root, parts) {
			for _, ext := range workflowExts {
				cand := stem + ext
				if _, err := os.Stat(cand); err == nil {
					return cand, nil
				}
			}
		}
	}
	return "", fmt.Errorf("workflow %q not found in any registry (searched %s); run `loom workflows ls` to list available workflows", arg, strings.Join(roots, ", "))
}

// workflowExts is the workflow file extensions, in precedence order: a name
// resolves to its `.yaml` form before its `.yml` form, and walkRegistry treats
// the same order as canonical when both exist. Centralized here so the two
// resolution paths cannot drift.
var workflowExts = []string{".yaml", ".yml"}

// isWorkflowExt reports whether ext (as returned by filepath.Ext, leading dot
// included) names a workflow file.
func isWorkflowExt(ext string) bool {
	return slices.Contains(workflowExts, ext)
}

// candidateStems returns the extensionless paths a registry name resolves to
// within one root, in precedence order: the flat file `<root>/<parts...>` before
// the eponymous-dir form `<root>/<parts...>/<last>`, so a flat file shadows a
// dir form on the same name. walkRegistry encodes the identical flat-over-dir
// precedence; TestRegistryResolveWalkParity pins the two together.
//
// The per-component checks in splitWorkflowName already forbid any component
// that could climb out of the root ("", ".", "..", or one containing a
// separator), so these joins cannot escape lexically and need no further
// traversal guard. The dir form only repeats the final (already-validated)
// component, so it is equally safe.
func candidateStems(root string, parts []string) []string {
	flat := filepath.Join(append([]string{root}, parts...)...)
	dir := filepath.Join(flat, parts[len(parts)-1])
	return []string{flat, dir}
}

// splitWorkflowName parses a registry name into its colon-separated path
// components, stripping a trailing .yaml/.yml on the final component. It rejects
// empty, '.', and '..' components and any component containing a path separator,
// so the result can be joined under a registry root without escaping it.
func splitWorkflowName(arg string) ([]string, error) {
	parts := strings.Split(arg, ":")
	last := len(parts) - 1
	parts[last] = strings.TrimSuffix(parts[last], ".yaml")
	parts[last] = strings.TrimSuffix(parts[last], ".yml")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `/\`) {
			return nil, fmt.Errorf("invalid workflow name %q: component %q not allowed", arg, p)
		}
	}
	return parts, nil
}

// isRegistryName reports whether arg is a registry name rather than a filesystem
// path. A path separator marks a path (so a Windows drive path like `C:\wf.yaml`
// is a path despite its ':'); otherwise a ':' means a name, a .yaml/.yml suffix
// marks a path, and anything else is a flat name.
func isRegistryName(arg string) bool {
	if strings.ContainsAny(arg, `/\`) {
		return false
	}
	if strings.Contains(arg, ":") {
		return true
	}
	return !strings.HasSuffix(arg, ".yaml") && !strings.HasSuffix(arg, ".yml")
}
