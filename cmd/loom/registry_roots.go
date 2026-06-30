package main

import (
	"fmt"
	"os"

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
