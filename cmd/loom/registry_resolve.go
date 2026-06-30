package main

import "github.com/mcuste/loom/pkg/registry"

// resolveWorkflowRef maps a `loom run`/`check` positional argument to a
// workflow YAML path. Path-mode args are returned verbatim; registry-name args
// are resolved against the ordered registry roots (nearest-local to global).
// See registry.Resolve for the full precedence rules.
func resolveWorkflowRef(arg string) (string, error) {
	roots, err := registrySearchRoots()
	if err != nil {
		return "", err
	}
	return registry.Resolve(roots, arg)
}
