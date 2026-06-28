package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mcuste/loom/pkg/workflow"
)

// loadWorkflow resolves a workflow ref to a file, reads and inlines its
// prompt_file references, parses it, links and statically validates its
// sub-workflows, and runs the routing check. It is the shared prelude behind
// `loom run` and the scheduler: a workflow that fails to load is rejected when
// the command is issued, not at fire time. It returns the parsed workflow, the
// inlined manifest bytes (what the store persists), and the resolved absolute
// path the schedule records so the daemon can reload it from its own cwd.
func loadWorkflow(ref string) (*workflow.Workflow, []byte, string, error) {
	path, err := resolveWorkflowRef(ref)
	if err != nil {
		return nil, nil, "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	// Inline `prompt_file:` references relative to the workflow's directory, then
	// parse the inlined bytes. The inlined manifest is what gets stored, so the
	// run record stays self-contained even if the referenced files later change.
	manifest, err := workflow.InlinePromptFiles(raw, filepath.Dir(path))
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s: %w", path, err)
	}
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return nil, nil, "", err
	}
	// Resolve and link any `workflow:` children from disk, then statically
	// validate them, so a bad sub-workflow ref fails before any model call.
	if err := linkSubWorkflows(wf, path, nil); err != nil {
		return nil, nil, "", err
	}
	if err := checkSubWorkflows(wf); err != nil {
		return nil, nil, "", err
	}
	// Parse is registry-free; run the routing check now that the registry is
	// populated and children are linked (ValidateRouting recurses into wf.Subs).
	if err := wf.ValidateRouting(); err != nil {
		return nil, nil, "", err
	}
	return wf, manifest, path, nil
}

// absPath returns the absolute form of p, falling back to p when resolution
// fails. The scheduler stores an absolute workflow path so the daemon reloads
// the same file regardless of its own working directory.
func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
