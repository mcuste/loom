package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mcuste/loom/pkg/workflow"
)

// linkSubWorkflows resolves every `workflow:` task in wf (recursively), filling
// wf.Subs. selfPath is wf's own resolved path; chain is the stack of canonical
// child paths walked to reach wf, used to detect cycles.
//
// Each sub-workflow ref is resolved (registry name via resolveWorkflowRef, or a
// path relative to wf's directory), read, prompt-file-inlined relative to the
// CHILD yaml, parsed, recursed into, and stored into wf.Subs under the task id.
// Children are resolved from disk here at every run/resume rather than frozen
// into the parent manifest.
func linkSubWorkflows(wf *workflow.Workflow, selfPath string, chain []string) error {
	self := canonicalPath(selfPath)
	if slices.Contains(chain, self) {
		return fmt.Errorf("sub-workflow cycle: %s", strings.Join(append(chain, self), " -> "))
	}
	chain = append(chain, self)

	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if !t.IsSubWorkflow() {
			continue
		}
		childPath, err := resolveSubWorkflowRef(t.Workflow, filepath.Dir(selfPath))
		if err != nil {
			return fmt.Errorf("task %q: %w", t.ID, err)
		}
		raw, err := os.ReadFile(childPath)
		if err != nil {
			return fmt.Errorf("task %q: read sub-workflow %q: %w", t.ID, t.Workflow, err)
		}
		// prompt_file refs in the child resolve beside the CHILD yaml.
		inlined, err := workflow.InlinePromptFiles(raw, filepath.Dir(childPath))
		if err != nil {
			return fmt.Errorf("task %q: %s: %w", t.ID, childPath, err)
		}
		child, err := workflow.Parse(inlined)
		if err != nil {
			return fmt.Errorf("task %q: %s: %w", t.ID, childPath, err)
		}
		if err := linkSubWorkflows(child, childPath, chain); err != nil {
			return fmt.Errorf("task %q: %w", t.ID, err)
		}
		if wf.Subs == nil {
			wf.Subs = make(map[workflow.TaskID]*workflow.Workflow, 1)
		}
		wf.Subs[t.ID] = child
	}
	return nil
}

// resolveSubWorkflowRef maps a task's `workflow:` ref to a file path. A registry
// NAME resolves through the same rules as `loom run <name>`; a PATH ref resolves
// relative to parentDir (the directory of the workflow that links it) unless it
// is already absolute.
func resolveSubWorkflowRef(ref, parentDir string) (string, error) {
	if isRegistryName(ref) {
		return resolveWorkflowRef(ref)
	}
	if filepath.IsAbs(ref) {
		return ref, nil
	}
	return filepath.Join(parentDir, ref), nil
}

// canonicalPath reduces p to a stable absolute form for cycle detection: it
// applies filepath.Abs and then EvalSymlinks, falling back to the best form
// available when a step fails (e.g. EvalSymlinks on a not-yet-read path).
func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// checkSubWorkflows statically validates every linked sub-workflow task in wf
// (recursively) so errors surface during the check phase, before any model
// call: each `with:` key must be a declared child param, every required child
// param must be covered, and the child's output task must resolve.
func checkSubWorkflows(wf *workflow.Workflow) error {
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if !t.IsSubWorkflow() {
			continue
		}
		child := wf.Subs[t.ID]
		if child == nil {
			return fmt.Errorf("task %q: sub-workflow %q not linked", t.ID, t.Workflow)
		}
		declared := make(map[workflow.ParamName]bool, len(child.Params))
		for _, p := range child.Params {
			declared[p.Name] = true
		}
		provided := make(map[workflow.ParamName]bool, len(t.With))
		for _, a := range t.With {
			if !declared[a.Name] {
				return fmt.Errorf("task %q: with: %q is not a param of sub-workflow %q", t.ID, a.Name, t.Workflow)
			}
			provided[a.Name] = true
		}
		for _, p := range child.Params {
			if p.Required && !provided[p.Name] {
				return fmt.Errorf("task %q: sub-workflow %q requires param %q not supplied via with", t.ID, t.Workflow, p.Name)
			}
		}
		if _, err := child.OutputTask(); err != nil {
			return fmt.Errorf("task %q: sub-workflow %q: %w", t.ID, t.Workflow, err)
		}
		if err := checkSubWorkflows(child); err != nil {
			return err
		}
	}
	return nil
}
