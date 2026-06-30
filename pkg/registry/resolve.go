package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve maps a registry-name argument to a workflow file path, searching
// roots in order (nearest first). If arg contains a path separator or ends
// in .yaml/.yml without a ':', it is treated as a filesystem PATH and
// returned verbatim. Otherwise it is treated as a colon-separated registry
// NAME and resolved against each root using the flat-over-dir, .yaml-over-.yml
// precedence encoded in candidateStems.
func Resolve(roots []string, arg string) (string, error) {
	if !isRegistryName(arg) {
		return arg, nil
	}
	parts, err := splitWorkflowName(arg)
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
	return "", fmt.Errorf("workflow %q not found in any registry (searched %s); run `loom workflows ls` to list available workflows",
		arg, strings.Join(roots, ", "))
}

// isRegistryName reports whether arg is a registry name rather than a
// filesystem path. A path separator marks a path (so a Windows drive path
// like `C:\wf.yaml` is a path despite its ':'); otherwise a ':' means a
// name, a .yaml/.yml suffix marks a path, and anything else is a flat name.
func isRegistryName(arg string) bool {
	if strings.ContainsAny(arg, `/\`) {
		return false
	}
	if strings.Contains(arg, ":") {
		return true
	}
	return !strings.HasSuffix(arg, ".yaml") && !strings.HasSuffix(arg, ".yml")
}

// splitWorkflowName parses a registry name into its colon-separated path
// components, stripping a trailing .yaml/.yml on the final component. It
// rejects empty, '.', and '..' components and any component containing a path
// separator, so the result can be joined under a registry root without
// escaping it.
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

// candidateStems returns the extensionless paths a registry name resolves to
// within one root, in precedence order. The flat form (index precedence(false)=0)
// is probed before the eponymous-dir form (index precedence(true)=1), so a
// flat file `<...>/<cn>.yaml` shadows `<...>/<cn>/<cn>.yaml` in the same root.
//
// The per-component checks in splitWorkflowName already forbid any component
// that could escape the root, so these joins are safe without extra traversal
// guards.
func candidateStems(root string, parts []string) []string {
	flat := filepath.Join(append([]string{root}, parts...)...)
	dir := filepath.Join(flat, parts[len(parts)-1])
	stems := make([]string, 2)
	stems[precedence(false)] = flat // flat wins: index 0
	stems[precedence(true)] = dir   // dir form: index 1
	return stems
}
