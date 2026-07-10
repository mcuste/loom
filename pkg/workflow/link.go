package workflow

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// SubRefResolver maps a `workflow:` task ref to a file path. ref is the raw
// string from the YAML; parentDir is the directory of the workflow file that
// contains the task. A registry-name ref resolves via the registry; a relative
// path ref is anchored to parentDir; an absolute path is returned unchanged.
type SubRefResolver func(ref, parentDir string) (string, error)

// Link resolves and links every `workflow:` task in wf (recursively),
// statically validates each linked child's `with:` bindings and required
// params, and leaves runtime-aware routing validation to the caller. selfPath
// is wf's own resolved file path; resolve is called for every sub-workflow ref
// encountered during linking. It is the integration step between Parse
// (filesystem-free) and execution.
func Link(wf *Workflow, selfPath string, resolve SubRefResolver) error {
	if err := linkSubWorkflows(wf, selfPath, nil, resolve); err != nil {
		return err
	}
	return checkSubWorkflows(wf)
}

// linkSubWorkflows resolves every `workflow:` task in wf (recursively),
// filling wf.Subs. selfPath is wf's own resolved path; chain is the stack of
// canonical child paths walked to reach wf, used to detect cycles.
//
// Each sub-workflow ref is resolved (registry name via resolve, or a path
// relative to wf's directory), read, prompt-file-inlined relative to the
// CHILD yaml, parsed, recursed into, and stored into wf.Subs under the task
// id. Children are resolved from disk here at every run/resume rather than
// frozen into the parent manifest.
func linkSubWorkflows(wf *Workflow, selfPath string, chain []string, resolve SubRefResolver) error {
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
		childPath, err := resolve(t.Workflow, filepath.Dir(selfPath))
		if err != nil {
			return fmt.Errorf("task %q: %w", t.ID, err)
		}
		// A child loads exactly as a top-level workflow does: its prompt_file refs
		// and relative script paths resolve beside the CHILD yaml. ReadAndParse is
		// the shared primitive for that read->inline->anchor->parse sequence.
		child, _, err := ReadAndParse(childPath)
		if err != nil {
			return fmt.Errorf("task %q: %w", t.ID, err)
		}
		// A task-level runtime/model/effort on the wrapper overrides the child's
		// workflow-level defaults, so a parent can run a shared child cheaper (e.g.
		// on a smaller model) without forking it. The child's per-task settings
		// still win via Effective; the caller's routing-validation pass re-validates
		// the child against the overridden defaults.
		applyChildOverrides(child, t)
		if err := linkSubWorkflows(child, childPath, chain, resolve); err != nil {
			return fmt.Errorf("task %q: %w", t.ID, err)
		}
		if wf.Subs == nil {
			wf.Subs = make(map[TaskID]*Workflow, 1)
		}
		wf.Subs[t.ID] = child
	}
	return nil
}

// applyChildOverrides copies any runtime/model/effort set on the wrapper task
// t onto child's workflow-level defaults, so child tasks that do not pin their
// own inherit the override via Workflow.Effective. An unset field on t leaves
// the child's own default in place.
func applyChildOverrides(child *Workflow, t *Task) {
	if t.Runtime != "" {
		child.Runtime = t.Runtime
		if child.hasDefinition {
			child.definition.Defaults.Runtime = t.Runtime
		}
	}
	if t.Model != "" {
		child.Model = t.Model
		if child.hasDefinition {
			child.definition.Defaults.Model = t.Model
		}
	}
	if t.Effort != "" {
		child.Effort = t.Effort
		if child.hasDefinition {
			child.definition.Defaults.Effort = t.Effort
		}
	}
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
func checkSubWorkflows(wf *Workflow) error {
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if !t.IsSubWorkflow() {
			continue
		}
		child := wf.Subs[t.ID]
		if child == nil {
			return fmt.Errorf("task %q: sub-workflow %q not linked", t.ID, t.Workflow)
		}
		declared := make(map[ParamName]bool, len(child.Params))
		for _, p := range child.Params {
			declared[p.Name] = true
		}
		provided := make(map[ParamName]bool, len(t.With))
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
