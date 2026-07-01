package workflowload

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mcuste/loom/pkg/registry"
	"github.com/mcuste/loom/pkg/workflow"
)

// Load resolves ref from the workflow registry-or-path boundary, normalizes
// the chosen file to an absolute path, reads and parses it, then links and
// statically validates any sub-workflow children.
func Load(home, cwd, ref string) (*workflow.Workflow, []byte, string, error) {
	path, err := resolveRef(home, cwd, ref)
	if err != nil {
		return nil, nil, "", err
	}
	path, err = absPath(cwd, path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve workflow path %s: %w", path, err)
	}
	wf, manifest, err := workflow.ReadAndParse(path)
	if err != nil {
		return nil, nil, "", err
	}
	if err := Link(home, cwd, path, wf); err != nil {
		return nil, nil, "", err
	}
	return wf, manifest, path, nil
}

// Link resolves, links, and statically validates any sub-workflow children
// referenced by wf using the same registry-or-path lookup rules as Load.
func Link(home, cwd, selfPath string, wf *workflow.Workflow) error {
	return workflow.Link(wf, selfPath, func(ref, parentDir string) (string, error) {
		return resolveSubRef(home, cwd, ref, parentDir)
	})
}

// List returns the merged registry workflows visible from cwd: nearest local
// roots first, then the global home registry last.
func List(home, cwd string) ([]registry.Ref, error) {
	roots, err := searchRoots(home, cwd)
	if err != nil {
		return nil, err
	}
	return registry.List(roots)
}

func searchRoots(home, cwd string) ([]string, error) {
	if cwd == "" {
		return nil, fmt.Errorf("resolve working directory: empty")
	}
	return append(registry.LocalDirs(cwd), workflowsDir(home)), nil
}

func resolveRef(home, cwd, ref string) (string, error) {
	if !isRegistryName(ref) {
		return ref, nil
	}
	roots, err := searchRoots(home, cwd)
	if err != nil {
		return "", err
	}
	return registry.Resolve(roots, ref)
}

func resolveSubRef(home, cwd, ref, parentDir string) (string, error) {
	resolved, err := resolveRef(home, cwd, ref)
	if err != nil {
		return "", err
	}
	if resolved == ref && !filepath.IsAbs(resolved) {
		return filepath.Join(parentDir, resolved), nil
	}
	return resolved, nil
}

func absPath(cwd, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if cwd == "" {
		return "", fmt.Errorf("working directory required for relative path %s", path)
	}
	return filepath.Abs(filepath.Join(cwd, path))
}

func workflowsDir(home string) string {
	return filepath.Join(home, "workflows")
}

func isRegistryName(ref string) bool {
	if strings.ContainsAny(ref, `/\`) {
		return false
	}
	if strings.Contains(ref, ":") {
		return true
	}
	return !strings.HasSuffix(ref, ".yaml") && !strings.HasSuffix(ref, ".yml")
}
