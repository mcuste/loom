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
	wf, manifest, err := readAndParse(path)
	if err != nil {
		return nil, nil, "", err
	}
	// Resolve and link any `workflow:` children from disk, statically validate
	// them, and run the routing check, so a bad sub-workflow ref or route fails
	// before any model call.
	if err := linkAndValidate(wf, path); err != nil {
		return nil, nil, "", err
	}
	return wf, manifest, path, nil
}

// readAndParse reads the workflow file at the resolved path, inlines its
// `prompt_file:` references relative to the file's directory, and parses the
// inlined bytes. The inlined manifest is what the store persists, so the run
// record stays self-contained even if the referenced files later change. It
// stops before linking and validation so callers that must obtain the manifest
// before a chdir (--resume-latest) can defer that tail.
func readAndParse(path string) (*workflow.Workflow, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := workflow.InlinePromptFiles(raw, filepath.Dir(path))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return nil, nil, err
	}
	return wf, manifest, nil
}

// linkAndValidate runs the post-parse validation tail shared by loadWorkflow,
// `loom check`, and resume: it resolves and links wf's `workflow:` children from
// disk (selfPath is wf's own resolved path, "" when it has no on-disk identity),
// statically validates them, then runs the routing check. Parse is
// registry-free, so ValidateRouting runs here once children are linked; it
// recurses into wf.Subs.
func linkAndValidate(wf *workflow.Workflow, selfPath string) error {
	if err := linkSubWorkflows(wf, selfPath, nil); err != nil {
		return err
	}
	if err := checkSubWorkflows(wf); err != nil {
		return err
	}
	return wf.ValidateRouting()
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
