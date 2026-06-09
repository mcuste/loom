package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mcuste/loom/pkg/runtime"
)

// Parse decodes a workflow YAML document and returns the validated Workflow.
//
// The decoder runs in known-fields mode: any top-level or task-level key not
// recognized by the current schema is rejected. This is what produces a clear
// error for sub-workflow constructs (inputs:, output:, workflow:, with:) that
// are present in some example files but not yet supported by the type.
//
// Validation pipeline, in order:
//
//  1. Workflow name and every task id satisfy [A-Za-z0-9_]+.
//  2. Task ids are unique.
//  3. Every task has a non-empty prompt.
//  4. Every depends_on entry names a known task and appears at most once.
//  5. Every {{id}} placeholder in a prompt is a member of that task's
//     depends_on. Placeholders are pure templating — they never extend the
//     dependency graph implicitly.
//  6. The task graph has no cycles.
//  7. The effective runtime/model/effort per task (task override falling back
//     to workflow defaults) is accepted by the registered runtime spec.
func Parse(data []byte) (*Workflow, error) {
	var raw rawWorkflow
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}

	id, err := NewWorkflowID(raw.Name)
	if err != nil {
		return nil, err
	}
	if len(raw.Tasks) == 0 {
		return nil, fmt.Errorf("workflow %q: %w", id, ErrNoTasks)
	}

	wf := &Workflow{
		ID:           id,
		Description:  raw.Description,
		Runtime:      runtime.Name(raw.Runtime),
		Model:        runtime.Model(raw.Model),
		Effort:       runtime.Effort(raw.Effort),
		SystemPrompt: raw.SystemPrompt,
		Tasks:        make([]Task, 0, len(raw.Tasks)),
		byID:         make(map[TaskID]int, len(raw.Tasks)),
	}

	ids := make(map[TaskID]struct{}, len(raw.Tasks))
	for _, rt := range raw.Tasks {
		tid, err := NewTaskID(rt.ID)
		if err != nil {
			return nil, err
		}
		if _, dup := ids[tid]; dup {
			return nil, &DuplicateTaskIDError{ID: tid}
		}
		ids[tid] = struct{}{}
	}

	for _, rt := range raw.Tasks {
		tid := TaskID(rt.ID)
		if rt.Prompt == "" {
			return nil, fmt.Errorf("task %q: %w", tid, ErrMissingPrompt)
		}
		deps, err := buildDeps(tid, rt.DependsOn, rt.Prompt, ids)
		if err != nil {
			return nil, err
		}
		wf.byID[tid] = len(wf.Tasks)
		wf.Tasks = append(wf.Tasks, Task{
			ID:          tid,
			Prompt:      rt.Prompt,
			Description: rt.Description,
			Runtime:     runtime.Name(rt.Runtime),
			Model:       runtime.Model(rt.Model),
			Effort:      runtime.Effort(rt.Effort),
			DependsOn:   deps,
		})
	}

	if cycle, ok := findCycle(wf); ok {
		return nil, &CycleError{Cycle: cycle}
	}

	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		rt, m, e := wf.Effective(t)
		req := runtime.Request{
			TaskID:       string(t.ID),
			Prompt:       t.Prompt,
			Model:        m,
			Effort:       e,
			SystemPrompt: wf.SystemPrompt,
		}
		if err := runtime.Validate(rt, req); err != nil {
			return nil, fmt.Errorf("task %q: %w", t.ID, err)
		}
	}

	return wf, nil
}

// ParseFile reads path and parses it as a workflow YAML.
func ParseFile(path string) (*Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	wf, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return wf, nil
}

// rawWorkflow mirrors the YAML schema as decoded by yaml.v3. It exists only so
// the parser can apply its own validation; callers see the validated Workflow.
type rawWorkflow struct {
	Name         string    `yaml:"name"`
	Description  string    `yaml:"description"`
	Runtime      string    `yaml:"runtime"`
	Model        string    `yaml:"model"`
	Effort       string    `yaml:"effort"`
	SystemPrompt string    `yaml:"system_prompt"`
	Tasks        []rawTask `yaml:"tasks"`
}

type rawTask struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Runtime     string   `yaml:"runtime"`
	Model       string   `yaml:"model"`
	Effort      string   `yaml:"effort"`
	Prompt      string   `yaml:"prompt"`
	DependsOn   []string `yaml:"depends_on"`
}

// Sentinel parse errors. Typed errors below cover structured failures with
// fields the caller may want to inspect.
var (
	ErrNoTasks       = errors.New("workflow has no tasks")
	ErrMissingPrompt = errors.New("task has no prompt")
)

// DuplicateTaskIDError reports two tasks declaring the same id.
type DuplicateTaskIDError struct{ ID TaskID }

func (e *DuplicateTaskIDError) Error() string {
	return fmt.Sprintf("duplicate task id %q", e.ID)
}

// UnknownDependencyError reports a depends_on entry that does not match any
// task id in the workflow.
type UnknownDependencyError struct {
	Task TaskID
	Dep  TaskID
}

func (e *UnknownDependencyError) Error() string {
	return fmt.Sprintf("task %q: depends on unknown task %q", e.Task, e.Dep)
}

// UnknownPlaceholderError reports a {{x}} placeholder in a prompt whose name
// does not appear in the task's depends_on. Placeholders are not allowed to
// implicitly extend the dependency graph: every templated id must be declared
// up front so the DAG is unambiguous.
type UnknownPlaceholderError struct {
	Task TaskID
	Name string
}

func (e *UnknownPlaceholderError) Error() string {
	return fmt.Sprintf("task %q: placeholder {{%s}} not declared in depends_on", e.Task, e.Name)
}

// DuplicateDependencyError reports a task whose depends_on list names the
// same task more than once.
type DuplicateDependencyError struct {
	Task TaskID
	Dep  TaskID
}

func (e *DuplicateDependencyError) Error() string {
	return fmt.Sprintf("task %q: depends_on lists %q more than once", e.Task, e.Dep)
}

// CycleError reports a dependency cycle. Cycle lists the task ids forming the
// cycle in traversal order; the final element is the same as the first.
type CycleError struct{ Cycle []TaskID }

func (e *CycleError) Error() string {
	ids := make([]string, len(e.Cycle))
	for i, id := range e.Cycle {
		ids[i] = string(id)
	}
	return "dependency cycle: " + strings.Join(ids, " -> ")
}

// buildDeps validates a task's depends_on list and checks that every
// `{{x}}` placeholder in its prompt references an entry in that list.
//
// depends_on is the single source of truth for the dependency graph; the
// parser never extends it implicitly from prompt text. Repeating a
// placeholder in the prompt body (e.g. `{{a}}` twice) is fine — placeholders
// are templating, not dependency declarations.
//
// Self-edges are kept so findCycle reports them uniformly as a cycle of
// length 1; suppressing them here would hide the user error.
func buildDeps(tid TaskID, declared []string, prompt string, known map[TaskID]struct{}) ([]TaskID, error) {
	deps := make([]TaskID, 0, len(declared))
	declaredSet := make(map[TaskID]struct{}, len(declared))

	for _, raw := range declared {
		d, err := NewTaskID(raw)
		if err != nil {
			return nil, fmt.Errorf("task %q depends_on: %w", tid, err)
		}
		if _, ok := known[d]; !ok {
			return nil, &UnknownDependencyError{Task: tid, Dep: d}
		}
		if _, dup := declaredSet[d]; dup {
			return nil, &DuplicateDependencyError{Task: tid, Dep: d}
		}
		declaredSet[d] = struct{}{}
		deps = append(deps, d)
	}

	for _, m := range placeholderRe.FindAllStringSubmatch(prompt, -1) {
		if _, ok := declaredSet[TaskID(m[1])]; !ok {
			return nil, &UnknownPlaceholderError{Task: tid, Name: m[1]}
		}
	}
	return deps, nil
}

// findCycle runs a DFS over the dependency graph and returns the first cycle
// it discovers. The returned slice begins and ends with the same task id; the
// boolean is false when the graph is acyclic.
func findCycle(wf *Workflow) ([]TaskID, bool) {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[TaskID]int, len(wf.Tasks))
	adj := make(map[TaskID][]TaskID, len(wf.Tasks))
	for _, t := range wf.Tasks {
		adj[t.ID] = t.DependsOn
	}

	var stack []TaskID
	var cycle []TaskID

	var dfs func(TaskID) bool
	dfs = func(u TaskID) bool {
		color[u] = gray
		stack = append(stack, u)
		for _, v := range adj[u] {
			switch color[v] {
			case gray:
				for i, n := range stack {
					if n == v {
						cycle = append([]TaskID{}, stack[i:]...)
						cycle = append(cycle, v)
						return true
					}
				}
			case white:
				if dfs(v) {
					return true
				}
			}
		}
		color[u] = black
		stack = stack[:len(stack)-1]
		return false
	}

	for _, t := range wf.Tasks {
		if color[t.ID] == white && dfs(t.ID) {
			return cycle, true
		}
	}
	return nil, false
}
