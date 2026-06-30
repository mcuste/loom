package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mcuste/loom/pkg/registry"
)

// registrySearchRoots returns the ordered list of registry roots searched for
// a workflow name: the project-local .loom/workflows directories walking up
// from the current working directory to the git root, then the global
// $LOOM_HOME/workflows last.
func registrySearchRoots() ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	home, err := loomHome()
	if err != nil {
		return nil, err
	}
	return append(registry.LocalDirs(cwd), workflowsDir(home)), nil
}

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

// resolveSubWorkflowRef maps a task's `workflow:` ref to a file path.
// resolveWorkflowRef returns path-mode args verbatim, so when the returned
// value equals ref the arg was a filesystem path that needs anchoring to
// parentDir (unless it is already absolute).
func resolveSubWorkflowRef(ref, parentDir string) (string, error) {
	resolved, err := resolveWorkflowRef(ref)
	if err != nil {
		return "", err
	}
	// Path-mode: resolveWorkflowRef returned the arg unchanged. Anchor
	// relative paths against the parent workflow's directory.
	if resolved == ref && !filepath.IsAbs(resolved) {
		return filepath.Join(parentDir, resolved), nil
	}
	return resolved, nil
}

// listRegistryWorkflows resolves the ordered registry roots and walks them into
// the merged local+global workflow set. It is the shared prelude behind
// completion, `workflows ls`, and `schedule sync`.
func listRegistryWorkflows() ([]registry.Ref, error) {
	roots, err := registrySearchRoots()
	if err != nil {
		return nil, err
	}
	return registry.List(roots)
}
