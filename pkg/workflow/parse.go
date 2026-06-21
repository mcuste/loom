package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
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
//  3. Param block: names are valid, unique, required-vs-default is exclusive,
//     defaults are scalar strings.
//  4. Every task sets exactly one of `prompt:` or `command:`. A task with
//     `command:` (a shell task) must not set task-level runtime, model, or
//     effort.
//  5. Every depends_on entry names a known task and appears at most once.
//  6. Every {{id}} placeholder in a prompt or command is a member of that
//     task's depends_on. Placeholders are pure templating — they never extend
//     the dependency graph implicitly.
//  7. Every {{params.x}} placeholder (in prompt, command, or system_prompt)
//     references a declared param.
//  8. The task graph has no cycles.
//  9. Every prompt, command, and the system_prompt are free of malformed
//     `{{params.…}}` tokens; system_prompt is free of task-id placeholders.
//  10. Every declared param is referenced by at least one prompt, command, or
//     system_prompt.
//  11. The effective runtime/model/effort per LLM task is accepted by the
//     registered runtime spec. Shell tasks bypass this check.
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

	rawLoops, err := parseRawLoops(raw.Loops)
	if err != nil {
		return nil, err
	}

	// A zero top-level task set is rejected, but the sentinel depends on whether
	// any loops are declared: an empty workflow gets ErrNoTasks, while a
	// loops-only workflow gets the clearer ErrLoopsWithoutTopLevelTask.
	if len(raw.Tasks) == 0 {
		if len(rawLoops) > 0 {
			return nil, fmt.Errorf("workflow %q: %w", id, ErrLoopsWithoutTopLevelTask)
		}
		return nil, fmt.Errorf("workflow %q: %w", id, ErrNoTasks)
	}

	params, paramIdx, err := parseParams(raw.Params)
	if err != nil {
		return nil, err
	}

	wf := &Workflow{
		ID:           id,
		Description:  raw.Description,
		Runtime:      runtime.Name(raw.Runtime),
		Model:        runtime.Model(raw.Model),
		Effort:       runtime.Effort(raw.Effort),
		SystemPrompt: raw.SystemPrompt,
		Cache:        raw.Cache,
		Params:       params,
		Tasks:        make([]Task, 0, len(raw.Tasks)),
		byID:         make(map[TaskID]int, len(raw.Tasks)),
		paramByName:  paramIdx,
	}

	// Set membership reused by buildDeps' param scan; the index map's value type
	// is irrelevant for membership, so wrap it once here.
	paramSet := make(map[ParamName]struct{}, len(paramIdx))
	for n := range paramIdx {
		paramSet[n] = struct{}{}
	}

	// allTasks is the flat union of top-level and every loop's nested tasks, in
	// declaration order, each tagged with its owning loop ("" for top-level). The
	// whole parser runs over this list so wf.Tasks ends up flat and ordered, and
	// existing code over wf.Tasks (Plan, ByID, Effective, the scheduler) is
	// unchanged by the addition of scoped loops.
	type loopTask struct {
		rt   rawTask
		loop LoopID
	}
	allTasks := make([]loopTask, 0, len(raw.Tasks))
	for _, rt := range raw.Tasks {
		allTasks = append(allTasks, loopTask{rt: rt, loop: ""})
	}
	for _, rl := range rawLoops {
		for _, rt := range rl.tasks {
			allTasks = append(allTasks, loopTask{rt: rt, loop: rl.id})
		}
	}

	// Global task-id uniqueness across top-level and every loop's nested tasks: a
	// task lives in a single flat namespace regardless of which loop defines it.
	ids := make(map[TaskID]struct{}, len(allTasks))
	for _, lt := range allTasks {
		tid, err := NewTaskID(lt.rt.ID)
		if err != nil {
			return nil, err
		}
		if _, dup := ids[tid]; dup {
			return nil, &DuplicateTaskIDError{ID: tid}
		}
		ids[tid] = struct{}{}
	}

	// Loop ids share the global namespace: each must be distinct from every task
	// id and param name, and unique across loops.
	seenLoops := make(map[LoopID]struct{}, len(rawLoops))
	for _, rl := range rawLoops {
		if _, ok := ids[TaskID(rl.id)]; ok {
			return nil, &LoopIDCollisionError{Loop: rl.id, Kind: "task"}
		}
		if _, ok := paramIdx[ParamName(rl.id)]; ok {
			return nil, &LoopIDCollisionError{Loop: rl.id, Kind: "param"}
		}
		if _, dup := seenLoops[rl.id]; dup {
			return nil, &DuplicateLoopIDError{Loop: rl.id}
		}
		seenLoops[rl.id] = struct{}{}
	}

	// depsByID feeds the per-loop connectivity check; it is built from the raw
	// depends_on text (unknown-dependency rejection happens later in buildDeps).
	depsByID := make(map[TaskID][]TaskID, len(allTasks))
	for _, lt := range allTasks {
		ds := make([]TaskID, 0, len(lt.rt.DependsOn))
		for _, d := range lt.rt.DependsOn {
			ds = append(ds, TaskID(d))
		}
		depsByID[TaskID(lt.rt.ID)] = ds
	}

	loops, memberByLoop, err := buildLoopGroups(rawLoops, depsByID)
	if err != nil {
		return nil, err
	}
	wf.Loops = loops

	for _, lt := range allTasks {
		rt := lt.rt
		tid := TaskID(rt.ID)
		switch {
		case rt.Prompt == "" && rt.Command == "":
			return nil, fmt.Errorf("task %q: %w", tid, ErrMissingPromptOrCommand)
		case rt.Prompt != "" && rt.Command != "":
			return nil, fmt.Errorf("task %q: %w", tid, ErrPromptAndCommandSet)
		}
		// body is the text that placeholder validation runs against;
		// substitution targets the same string at execution time.
		body := rt.Prompt
		if rt.Command != "" {
			body = rt.Command
			if rt.Runtime != "" || rt.Model != "" || rt.Effort != "" {
				return nil, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithRuntime)
			}
		}
		schema, err := parseSchema(rt.Schema)
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", tid, err)
		}
		if schema != nil && rt.Command != "" {
			return nil, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithSchema)
		}
		deps, err := buildDeps(tid, rt.DependsOn, body, ids, paramSet, rt.As)
		if err != nil {
			return nil, err
		}
		if err := checkMalformedParamPlaceholders(tid, body); err != nil {
			return nil, err
		}
		retry, err := parseRetry(tid, rt.Retry)
		if err != nil {
			return nil, err
		}
		if rt.WritesState != "" && !identifierRe.MatchString(rt.WritesState) {
			return nil, &InvalidWritesStateError{Task: tid, Key: rt.WritesState}
		}
		forEach, forEachSource, err := parseForEach(tid, rt.ForEach, rt.As, deps, ids, paramSet)
		if err != nil {
			return nil, err
		}
		taskBudget, err := parseBudget(rt.Budget)
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", tid, err)
		}
		// A `when:` expression may only reference this task's dependencies: the
		// executor evaluates it after the dependency gates close, so a reference
		// to any other task (or the task's own id) could read an output that has
		// not been written yet. Bounding ParseCondition by depSet rejects those
		// at load time.
		var cond *Condition
		if rt.When != "" {
			depSet := make(map[TaskID]bool, len(deps))
			for _, d := range deps {
				depSet[d] = true
			}
			cond, err = ParseCondition(rt.When, depSet)
			if err != nil {
				return nil, fmt.Errorf("task %q: %w", tid, err)
			}
		}
		wf.byID[tid] = len(wf.Tasks)
		wf.Tasks = append(wf.Tasks, Task{
			ID:            tid,
			Prompt:        rt.Prompt,
			Command:       rt.Command,
			Description:   rt.Description,
			Runtime:       runtime.Name(rt.Runtime),
			Model:         runtime.Model(rt.Model),
			Effort:        runtime.Effort(rt.Effort),
			DependsOn:     deps,
			When:          rt.When,
			Cond:          cond,
			Retry:         retry,
			WritesState:   rt.WritesState,
			ForEach:       forEach,
			ForEachSource: forEachSource,
			As:            rt.As,
			Budget:        taskBudget,
			Schema:        schema,
			Cache:         rt.Cache,
			Loop:          lt.loop,
		})
	}

	if err := checkPrevPlaceholders(wf, memberByLoop); err != nil {
		return nil, err
	}

	if err := validateSystemPrompt(wf.SystemPrompt, paramSet); err != nil {
		return nil, err
	}

	if cycle, ok := findCycle(wf); ok {
		return nil, &CycleError{Cycle: cycle}
	}

	if err := checkUnusedParams(wf); err != nil {
		return nil, err
	}

	loop, err := parseLoop(raw.Loop, ids)
	if err != nil {
		return nil, err
	}
	wf.Loop = loop

	budget, err := parseBudget(raw.Budget)
	if err != nil {
		return nil, err
	}
	wf.Budget = budget

	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if t.IsShell() {
			// Shell tasks bypass the runtime registry entirely; runtime/model/
			// effort have no meaning, and the task-level reject above guarantees
			// they are unset on t.
			continue
		}
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
//
// Params is captured as a raw yaml.Node so parseParams can inspect each entry's
// `default:` scalar without yaml.v3 coercing `1` to !!int or `~` to !!null
// before validation runs. (Plain decoding into a typed struct would lose the
// distinction between `default: ""` and an absent key.)
type rawWorkflow struct {
	Name         string    `yaml:"name"`
	Description  string    `yaml:"description"`
	Runtime      string    `yaml:"runtime"`
	Model        string    `yaml:"model"`
	Effort       string    `yaml:"effort"`
	SystemPrompt string    `yaml:"system_prompt"`
	Params       yaml.Node `yaml:"params"`
	Tasks        []rawTask `yaml:"tasks"`
	// Loop is captured as a raw yaml.Node so the parser can distinguish an
	// absent `loop:` key (the workflow runs once) from a present block whose
	// fields must be validated against the task set.
	Loop yaml.Node `yaml:"loop"`
	// Loops is captured as a raw yaml.Node so the parser can walk each scoped-loop
	// entry, flatten its nested tasks into Workflow.Tasks, and validate ids,
	// connectivity, and convergence against the full task set. Absent key means
	// no scoped loops.
	Loops yaml.Node `yaml:"loops"`
	// Budget is captured as a raw yaml.Node so the parser can distinguish an
	// absent `budget:` key (no limit) from a present block whose max_cost_usd
	// must be validated as a positive float.
	Budget yaml.Node `yaml:"budget"`
	// Cache is the workflow-level memoization default. A plain bool suffices: an
	// absent `cache:` key decodes to false, which is exactly the "off unless a
	// task opts in" default.
	Cache bool `yaml:"cache"`
}

// rawTask mirrors the per-task YAML schema. It exists so the parser can apply
// its own validation before promoting values to the typed Task. Several fields
// are yaml.Node to let the parser distinguish an absent key (zero value, inherit
// default) from a present-but-partial block that must be validated.
type rawTask struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Runtime     string   `yaml:"runtime"`
	Model       string   `yaml:"model"`
	Effort      string   `yaml:"effort"`
	Prompt      string   `yaml:"prompt"`
	Command     string   `yaml:"command"`
	DependsOn   []string `yaml:"depends_on"`
	WritesState string   `yaml:"writes_state"`
	When        string   `yaml:"when"`
	As          string   `yaml:"as"`
	// Retry is captured as a raw yaml.Node so the parser can distinguish an
	// absent `retry:` key (zero value, no retry) from a present-but-partial
	// block whose `backoff`/`on` defaults must be filled in.
	Retry yaml.Node `yaml:"retry"`
	// ForEach is captured as a raw yaml.Node so parseForEach can tell a literal
	// sequence (static fanout) from a single-placeholder scalar (dynamic fanout)
	// and reject other shapes.
	ForEach yaml.Node `yaml:"for_each"`
	// Budget is captured as a raw yaml.Node so the parser can distinguish an
	// absent per-task `budget:` key (no limit) from a present block validated
	// the same way as the workflow-level budget.
	Budget yaml.Node `yaml:"budget"`
	// Schema is captured as a raw yaml.Node so the parser can distinguish an
	// absent per-task `schema:` key (no validation) from a present block whose
	// type/required/properties must be validated.
	Schema yaml.Node `yaml:"schema"`
	// Cache is a pointer so an absent `cache:` key (nil, inherit the workflow
	// default) is distinct from an explicit `cache: false` (opt out). Shell tasks
	// are never memoized regardless, so no shell-vs-LLM rejection applies here.
	Cache *bool `yaml:"cache"`
}

// rawParam mirrors the typed (non-default) fields of a single `params:`
// entry. `default:` is captured separately as a yaml.Node so the raw scalar
// text (e.g. `1` from `default: 1`) survives without yaml.v3 coercing it to
// !!int — see decodeRawParam.
type rawParam struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
}

// Sentinel parse errors. Typed errors below cover structured failures with
// fields the caller may want to inspect.
var (
	// ErrNoTasks is returned when the workflow YAML declares no tasks.
	ErrNoTasks = errors.New("workflow has no tasks")
	// ErrLoopsWithoutTopLevelTask is returned when a workflow declares `loops:`
	// blocks but no top-level `tasks:`. A scoped loop refines a workflow that
	// must have at least one top-level task to seed it.
	ErrLoopsWithoutTopLevelTask = errors.New("loops require at least one top-level task")
	// ErrMissingPrompt is returned when a task declares no prompt.
	ErrMissingPrompt = errors.New("task has no prompt")
	// ErrMissingParamName is returned when a params entry omits the name field.
	ErrMissingParamName = errors.New("param has no name")

	// ErrMissingPromptOrCommand reports a task that sets neither `prompt:` nor
	// `command:`. Exactly one must be present.
	ErrMissingPromptOrCommand = errors.New("task has neither prompt nor command")
	// ErrPromptAndCommandSet reports a task that sets both `prompt:` and
	// `command:`. The two are mutually exclusive.
	ErrPromptAndCommandSet = errors.New("task sets both prompt and command")
	// ErrShellTaskWithRuntime reports a shell task (one with `command:`) that
	// also sets task-level `runtime:`, `model:`, or `effort:`. These fields are
	// meaningless for shell tasks and rejected at the task level; workflow-level
	// defaults are tolerated.
	ErrShellTaskWithRuntime = errors.New("shell task must not set runtime, model, or effort")
	// ErrLoopMissingUntilEmpty reports a `loop:` block that omits the required
	// `until_empty` key naming the task whose empty output drains the loop.
	ErrLoopMissingUntilEmpty = errors.New("loop: until_empty is required")
)

// DuplicateTaskIDError reports two tasks declaring the same id.
type DuplicateTaskIDError struct{ ID TaskID }

func (e *DuplicateTaskIDError) Error() string {
	return fmt.Sprintf("duplicate task id %q", e.ID)
}

// InvalidWritesStateError reports a `writes_state` value that fails the
// `[A-Za-z0-9_]+` rule, the same alphabet as a state placeholder key.
type InvalidWritesStateError struct {
	Task TaskID
	Key  string
}

func (e *InvalidWritesStateError) Error() string {
	return fmt.Sprintf("task %q: invalid writes_state %q: must match [A-Za-z0-9_]+", e.Task, e.Key)
}

// UnknownLoopTaskError reports a `loop.until_empty` that names no task in the
// workflow; the loop could never observe its output and so could never drain.
type UnknownLoopTaskError struct{ Task TaskID }

func (e *UnknownLoopTaskError) Error() string {
	return fmt.Sprintf("loop: until_empty names unknown task %q", e.Task)
}

// InvalidLoopMaxError reports a `loop.max` below 1. Max caps the iteration
// count and must permit at least one pass.
type InvalidLoopMaxError struct{ Max int }

func (e *InvalidLoopMaxError) Error() string {
	return fmt.Sprintf("loop: invalid max %d: must be >= 1", e.Max)
}

// UnknownLoopFieldError reports a key inside the `loop:` mapping that is not
// one of until_empty|max.
type UnknownLoopFieldError struct{ Field string }

func (e *UnknownLoopFieldError) Error() string {
	return fmt.Sprintf("loop: unknown field %q", e.Field)
}

// MissingForEachAsError reports a `for_each:` task that omits the required
// `as:` key naming the per-instance loop variable.
type MissingForEachAsError struct{ Task TaskID }

func (e *MissingForEachAsError) Error() string {
	return fmt.Sprintf("task %q: for_each requires as", e.Task)
}

// ForEachAsWithoutForEachError reports an `as:` declared on a task that has no
// `for_each:`; the loop variable would bind nothing.
type ForEachAsWithoutForEachError struct{ Task TaskID }

func (e *ForEachAsWithoutForEachError) Error() string {
	return fmt.Sprintf("task %q: as set without for_each", e.Task)
}

// InvalidForEachAsError reports an `as:` value that fails the `[A-Za-z0-9_]+`
// rule, the same alphabet as a placeholder name.
type InvalidForEachAsError struct {
	Task TaskID
	As   string
}

func (e *InvalidForEachAsError) Error() string {
	return fmt.Sprintf("task %q: invalid as %q: must match [A-Za-z0-9_]+", e.Task, e.As)
}

// ForEachAsCollisionError reports an `as:` loop variable whose name collides
// with a task id or param name; Kind is "task" or "param".
type ForEachAsCollisionError struct {
	Task TaskID
	As   string
	Kind string
}

func (e *ForEachAsCollisionError) Error() string {
	return fmt.Sprintf("task %q: as %q collides with a %s name", e.Task, e.As, e.Kind)
}

// InvalidForEachSourceError reports a scalar `for_each:` value that is not
// exactly one `{{...}}` placeholder (the dynamic-fanout shape).
type InvalidForEachSourceError struct {
	Task   TaskID
	Source string
}

func (e *InvalidForEachSourceError) Error() string {
	return fmt.Sprintf("task %q: for_each source %q must be a single {{...}} placeholder", e.Task, e.Source)
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
//
// Hint, when non-empty, carries a clarifying suggestion appended to the error
// message — currently "did you mean {{params.<name>}}?" when the offending
// bare {{x}} matches a declared param.
type UnknownPlaceholderError struct {
	Task TaskID
	Name string
	Hint string
}

func (e *UnknownPlaceholderError) Error() string {
	msg := fmt.Sprintf("task %q: placeholder {{%s}} not declared in depends_on", e.Task, e.Name)
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	return msg
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

// DuplicateParamNameError reports two params declaring the same name.
type DuplicateParamNameError struct{ Name ParamName }

func (e *DuplicateParamNameError) Error() string {
	return fmt.Sprintf("duplicate param name %q", e.Name)
}

// ConflictingParamSpecError reports a param that sets both `required: true`
// and a `default:` — a default would never apply, so the spec is contradictory.
type ConflictingParamSpecError struct{ Name ParamName }

func (e *ConflictingParamSpecError) Error() string {
	return fmt.Sprintf("param %q: required and default are mutually exclusive", e.Name)
}

// InvalidParamDefaultError reports a param `default:` that fails the
// scalar-string rule (non-scalar YAML node, explicit null, etc.).
type InvalidParamDefaultError struct {
	Name   ParamName
	Reason string
}

func (e *InvalidParamDefaultError) Error() string {
	return fmt.Sprintf("param %q: invalid default: %s", e.Name, e.Reason)
}

// UnknownParamError reports a `{{params.X}}` placeholder whose name is not
// declared in the workflow's `params:` block.
type UnknownParamError struct {
	Task TaskID
	Name string
}

func (e *UnknownParamError) Error() string {
	return fmt.Sprintf("task %q: placeholder {{params.%s}} references undeclared param", e.Task, e.Name)
}

// MalformedParamPlaceholderError reports a `{{params.…}}` token that does not
// match the strict `{{params.name}}` shape — typically `{{params.x.y}}` or
// `{{ params.x }}` with stray whitespace.
type MalformedParamPlaceholderError struct {
	Task  TaskID
	Token string
}

func (e *MalformedParamPlaceholderError) Error() string {
	return fmt.Sprintf("task %q: malformed param placeholder %q", e.Task, e.Token)
}

// SystemPlaceholderTaskRefError reports a `{{taskid}}` placeholder in the
// workflow-level system_prompt. No task can be a dependency of system_prompt,
// so a task reference there is always unresolvable.
type SystemPlaceholderTaskRefError struct {
	Name string
}

func (e *SystemPlaceholderTaskRefError) Error() string {
	return fmt.Sprintf("system_prompt: placeholder {{%s}} references a task; system_prompt has no task dependencies", e.Name)
}

// UnusedParamError reports a declared param that no prompt or system_prompt
// references.
type UnusedParamError struct {
	Name ParamName
}

func (e *UnusedParamError) Error() string {
	return fmt.Sprintf("param %q is declared but never referenced", e.Name)
}

// InvalidRetryMaxError reports a task `retry.max` that is negative. Max counts
// retries after the first attempt, so it must be >= 0.
type InvalidRetryMaxError struct {
	Task TaskID
	Max  int
}

func (e *InvalidRetryMaxError) Error() string {
	return fmt.Sprintf("task %q: invalid retry max %d: must be >= 0", e.Task, e.Max)
}

// UnknownBackoffError reports a task `retry.backoff` that is not one of
// none|constant|exponential.
type UnknownBackoffError struct {
	Task    TaskID
	Backoff string
}

func (e *UnknownBackoffError) Error() string {
	return fmt.Sprintf("task %q: unknown retry backoff %q: must be one of none|constant|exponential", e.Task, e.Backoff)
}

// UnknownRetryClassError reports a task `retry.on` entry that names an error
// class the classifier does not recognize. The recognized vocabulary is
// sourced from RetryClasses so the message cannot drift.
type UnknownRetryClassError struct {
	Task  TaskID
	Class string
}

func (e *UnknownRetryClassError) Error() string {
	return fmt.Sprintf("task %q: unknown retry class %q: only %s is recognized", e.Task, e.Class, recognizedRetryClasses())
}

// UnknownRetryFieldError reports a key inside a task `retry:` mapping that is
// not one of max|backoff|on.
type UnknownRetryFieldError struct {
	Task  TaskID
	Field string
}

func (e *UnknownRetryFieldError) Error() string {
	return fmt.Sprintf("task %q: retry: unknown field %q", e.Task, e.Field)
}

// parseParams validates the raw `params:` block and returns the resolved
// Params slice in declaration order plus an index from name → slice position.
//
// node is the top-level `params:` yaml.Node — either zero (no `params:` key),
// a sequence, or anything else (which is a structural error). Walking the
// node by hand (rather than relying on `[]rawParam` decoding) preserves the
// raw default scalar text and lets the parser reject non-scalar / null
// defaults precisely.
func parseParams(node yaml.Node) ([]Param, map[ParamName]int, error) {
	if node.Kind == 0 {
		return nil, nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, nil, fmt.Errorf("params: must be a sequence of param entries")
	}
	params := make([]Param, 0, len(node.Content))
	idx := make(map[ParamName]int, len(node.Content))
	for _, entry := range node.Content {
		rp, defNode, err := decodeRawParam(entry)
		if err != nil {
			return nil, nil, err
		}
		if rp.Name == "" {
			return nil, nil, ErrMissingParamName
		}
		name, err := NewParamName(rp.Name)
		if err != nil {
			return nil, nil, err
		}
		if _, dup := idx[name]; dup {
			return nil, nil, &DuplicateParamNameError{Name: name}
		}
		p := Param{
			Name:        name,
			Description: rp.Description,
			Required:    rp.Required,
		}
		if defNode != nil {
			if rp.Required {
				return nil, nil, &ConflictingParamSpecError{Name: name}
			}
			if defNode.Kind != yaml.ScalarNode {
				return nil, nil, &InvalidParamDefaultError{Name: name, Reason: "must be a scalar string"}
			}
			if defNode.Tag == "!!null" {
				return nil, nil, &InvalidParamDefaultError{Name: name, Reason: "null default is not allowed"}
			}
			p.Default = defNode.Value
			p.HasDefault = true
		}
		idx[name] = len(params)
		params = append(params, p)
	}
	return params, idx, nil
}

// decodeRawParam destructures a single `params:` mapping entry. Returned
// values: the typed fields (name/description/required) for plain access, the
// `default:` value node (nil when absent), or an error for an unknown key or
// shape mismatch.
func decodeRawParam(entry *yaml.Node) (rawParam, *yaml.Node, error) {
	var rp rawParam
	if entry.Kind != yaml.MappingNode {
		return rp, nil, fmt.Errorf("params: entry must be a mapping")
	}
	var defNode *yaml.Node
	for i := 0; i+1 < len(entry.Content); i += 2 {
		k, v := entry.Content[i], entry.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return rp, nil, fmt.Errorf("params: entry key must be a scalar")
		}
		switch k.Value {
		case "name":
			if err := v.Decode(&rp.Name); err != nil {
				return rp, nil, fmt.Errorf("params.name: %w", err)
			}
		case "description":
			if err := v.Decode(&rp.Description); err != nil {
				return rp, nil, fmt.Errorf("params.description: %w", err)
			}
		case "required":
			if err := v.Decode(&rp.Required); err != nil {
				return rp, nil, fmt.Errorf("params.required: %w", err)
			}
		case "default":
			defNode = v
		default:
			return rp, nil, fmt.Errorf("params: unknown field %q", k.Value)
		}
	}
	return rp, defNode, nil
}

// parseRetry decodes a task's `retry:` mapping into a Retry policy. An absent
// block (zero-value node) yields the zero-value Retry (no retry). A present
// block defaults backoff to exponential and on to [transient] when omitted,
// then validates max >= 0, backoff against the enum, and every on entry against
// the known error classes.
func parseRetry(tid TaskID, node yaml.Node) (Retry, error) {
	if node.Kind == 0 {
		return Retry{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return Retry{}, fmt.Errorf("task %q: retry must be a mapping", tid)
	}
	r := Retry{Backoff: BackoffExponential, On: []string{string(RetryClassTransient)}}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return Retry{}, fmt.Errorf("task %q: retry key must be a scalar", tid)
		}
		switch k.Value {
		case "max":
			if err := v.Decode(&r.Max); err != nil {
				return Retry{}, fmt.Errorf("task %q: retry.max: %w", tid, err)
			}
			if r.Max < 0 {
				return Retry{}, &InvalidRetryMaxError{Task: tid, Max: r.Max}
			}
		case "backoff":
			var b string
			if err := v.Decode(&b); err != nil {
				return Retry{}, fmt.Errorf("task %q: retry.backoff: %w", tid, err)
			}
			switch Backoff(b) {
			case BackoffNone, BackoffConstant, BackoffExponential:
				r.Backoff = Backoff(b)
			default:
				return Retry{}, &UnknownBackoffError{Task: tid, Backoff: b}
			}
		case "on":
			var on []string
			if err := v.Decode(&on); err != nil {
				return Retry{}, fmt.Errorf("task %q: retry.on: %w", tid, err)
			}
			for _, c := range on {
				if !ValidRetryClass(RetryClass(c)) {
					return Retry{}, &UnknownRetryClassError{Task: tid, Class: c}
				}
			}
			r.On = on
		default:
			return Retry{}, &UnknownRetryFieldError{Task: tid, Field: k.Value}
		}
	}
	return r, nil
}

// parseLoop decodes the workflow-level `loop:` mapping into a *Loop. An absent
// block (zero-value node) yields nil — the workflow runs exactly once. A
// present block requires `until_empty` to name a known task and `max` to be a
// positive integer; both fields are mandatory.
func parseLoop(node yaml.Node, known map[TaskID]struct{}) (*Loop, error) {
	if node.Kind == 0 {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("loop: must be a mapping")
	}
	l := &Loop{}
	hasUntil := false
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return nil, errors.New("loop: key must be a scalar")
		}
		switch k.Value {
		case "until_empty":
			var s string
			if err := v.Decode(&s); err != nil {
				return nil, fmt.Errorf("loop.until_empty: %w", err)
			}
			tid, err := NewTaskID(s)
			if err != nil {
				return nil, err
			}
			if _, ok := known[tid]; !ok {
				return nil, &UnknownLoopTaskError{Task: tid}
			}
			l.UntilEmpty = tid
			hasUntil = true
		case "max":
			if err := v.Decode(&l.Max); err != nil {
				return nil, fmt.Errorf("loop.max: %w", err)
			}
		default:
			return nil, &UnknownLoopFieldError{Field: k.Value}
		}
	}
	if !hasUntil {
		return nil, ErrLoopMissingUntilEmpty
	}
	if l.Max < 1 {
		return nil, &InvalidLoopMaxError{Max: l.Max}
	}
	return l, nil
}

// parseForEach decodes a task's `for_each:` node into a fanout spec. An absent
// node (zero value) means no fanout — in which case a stray `as:` is rejected,
// since the loop variable would bind nothing.
//
// A YAML sequence is a static fanout: its scalar entries become the literal
// values (returned as forEach, possibly empty but non-nil). A YAML scalar is a
// dynamic fanout: it must hold exactly one `{{...}}` placeholder, returned as
// source; a `{{id}}` source must name a depends_on entry and a `{{params.x}}`
// source a declared param (state sources need neither). Any other shape is an
// error.
//
// When a fanout is present, `as` is required, must satisfy the identifier
// alphabet, and must not collide with a task id or param name.
func parseForEach(tid TaskID, node yaml.Node, as string, deps []TaskID, known map[TaskID]struct{}, params map[ParamName]struct{}) (forEach []string, source string, err error) {
	if node.Kind == 0 {
		if as != "" {
			return nil, "", &ForEachAsWithoutForEachError{Task: tid}
		}
		return nil, "", nil
	}
	if as == "" {
		return nil, "", &MissingForEachAsError{Task: tid}
	}
	if !identifierRe.MatchString(as) {
		return nil, "", &InvalidForEachAsError{Task: tid, As: as}
	}
	if _, ok := known[TaskID(as)]; ok {
		return nil, "", &ForEachAsCollisionError{Task: tid, As: as, Kind: "task"}
	}
	if _, ok := params[ParamName(as)]; ok {
		return nil, "", &ForEachAsCollisionError{Task: tid, As: as, Kind: "param"}
	}

	switch node.Kind {
	case yaml.SequenceNode:
		vals := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, "", fmt.Errorf("task %q: for_each: list entries must be scalars", tid)
			}
			vals = append(vals, item.Value)
		}
		return vals, "", nil
	case yaml.ScalarNode:
		src := node.Value
		taskRefs, paramRefs, stateRefs := scanPlaceholders(src)
		if len(taskRefs)+len(paramRefs)+len(stateRefs) != 1 {
			return nil, "", &InvalidForEachSourceError{Task: tid, Source: src}
		}
		if len(taskRefs) == 1 {
			name := taskRefs[0]
			if !slices.Contains(deps, TaskID(name)) {
				return nil, "", &UnknownPlaceholderError{Task: tid, Name: name}
			}
		}
		if len(paramRefs) == 1 {
			if _, ok := params[ParamName(paramRefs[0])]; !ok {
				return nil, "", &UnknownParamError{Task: tid, Name: paramRefs[0]}
			}
		}
		return nil, src, nil
	default:
		return nil, "", fmt.Errorf("task %q: for_each must be a list or a single {{...}} placeholder", tid)
	}
}

// buildDeps validates a task's depends_on list and checks that every
// `{{x}}` and `{{params.x}}` placeholder in its prompt is well-defined.
//
// depends_on is the single source of truth for the dependency graph; the
// parser never extends it implicitly from prompt text. Repeating a
// placeholder in the prompt body (e.g. `{{a}}` twice) is fine — placeholders
// are templating, not dependency declarations.
//
// Self-edges are kept so findCycle reports them uniformly as a cycle of
// length 1; suppressing them here would hide the user error.
//
// When a bare `{{x}}` placeholder is unknown to depends_on but happens to
// match a declared param, the returned UnknownPlaceholderError carries a
// hint suggesting `{{params.x}}` so users notice the missing prefix.
//
// loopVar, when non-empty, is a `for_each` task's `as` variable: a `{{loopVar}}`
// placeholder is resolved per-instance at run time, not via the DAG, so it is
// excluded from the task-ref check (it creates no dependency edge).
func buildDeps(tid TaskID, declared []string, prompt string, known map[TaskID]struct{}, params map[ParamName]struct{}, loopVar string) ([]TaskID, error) {
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

	// Task refs first, then param refs: this order makes the first error
	// returned byte-identical to the pre-refactor two-loop scan. State refs
	// are ignored here: they need no declaration and create no dependency edge.
	taskRefs, paramRefs, _ := scanPlaceholders(prompt)
	for _, name := range taskRefs {
		if name == loopVar {
			continue
		}
		if _, ok := declaredSet[TaskID(name)]; ok {
			continue
		}
		err := &UnknownPlaceholderError{Task: tid, Name: name}
		if _, isParam := params[ParamName(name)]; isParam {
			err.Hint = fmt.Sprintf("did you mean {{params.%s}}?", name)
		}
		return nil, err
	}

	for _, name := range paramRefs {
		if _, ok := params[ParamName(name)]; !ok {
			return nil, &UnknownParamError{Task: tid, Name: name}
		}
	}
	return deps, nil
}

// scanPlaceholders walks text in a SINGLE pass with combinedPlaceholderRe and
// returns the task-id, param, and state placeholder names in source order. The
// combined regex disambiguates `{{params.x}}` (group 1), `{{state.x}}` (group
// 2), and `{{id}}` (group 3), so the three slices never cross-contaminate.
// State refs are returned separately so callers can treat them as neither task
// edges nor param references: they need no declaration and create no DAG edge.
func scanPlaceholders(text string) (taskRefs, paramRefs, stateRefs []string) {
	for _, m := range combinedPlaceholderRe.FindAllStringSubmatch(text, -1) {
		// Exactly one capture group is non-empty per match: group 1 is the param
		// name, group 2 is the state key, group 3 is the bare task id.
		switch {
		case m[1] != "":
			paramRefs = append(paramRefs, m[1])
		case m[2] != "":
			stateRefs = append(stateRefs, m[2])
		default:
			taskRefs = append(taskRefs, m[3])
		}
	}
	return taskRefs, paramRefs, stateRefs
}

// brokenBraceRe matches any `{{...}}` token whose body contains no closing
// braces. Used together with combinedPlaceholderRe to spot tokens that look
// like placeholders but fail the strict `{{name}}` / `{{params.name}}` shape.
var brokenBraceRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

// checkMalformedParamPlaceholders scans prompt for any `{{params.…}}`-shaped
// token that combinedPlaceholderRe rejects — typically `{{params.x.y}}` or
// `{{ params.x }}` — and reports it. Other malformed `{{…}}` tokens (bare,
// non-param shapes) fall through to buildDeps' UnknownPlaceholderError path.
func checkMalformedParamPlaceholders(tid TaskID, prompt string) error {
	for _, tok := range brokenBraceRe.FindAllString(prompt, -1) {
		if combinedPlaceholderRe.MatchString(tok) {
			continue
		}
		// Strip surrounding whitespace so `{{ params.x }}` is recognized as a
		// would-be param placeholder. `{{params}}` alone is left to fall through
		// to buildDeps' UnknownPlaceholderError path.
		inner := strings.TrimSpace(tok[2 : len(tok)-2])
		if strings.HasPrefix(inner, "params.") {
			return &MalformedParamPlaceholderError{Task: tid, Token: tok}
		}
	}
	return nil
}

// validateSystemPrompt rejects task-id placeholders in the workflow-level
// system_prompt (no task can be its dependency) and rejects unknown / malformed
// param placeholders there too.
func validateSystemPrompt(sp string, params map[ParamName]struct{}) error {
	if sp == "" {
		return nil
	}
	taskRefs, paramRefs, _ := scanPlaceholders(sp)
	// Task-id placeholders are never resolvable in system_prompt. State refs
	// are tolerated: they resolve against the cross-run state at run time.
	if len(taskRefs) > 0 {
		return &SystemPlaceholderTaskRefError{Name: taskRefs[0]}
	}
	for _, name := range paramRefs {
		if _, ok := params[ParamName(name)]; !ok {
			return &UnknownParamError{Task: "", Name: name}
		}
	}
	if err := checkMalformedParamPlaceholders("", sp); err != nil {
		return err
	}
	return nil
}

// checkUnusedParams enforces that every declared param is referenced by at
// least one prompt or by the system_prompt.
func checkUnusedParams(wf *Workflow) error {
	if len(wf.Params) == 0 {
		return nil
	}
	used := make(map[ParamName]struct{}, len(wf.Params))
	scan := func(s string) {
		_, paramRefs, _ := scanPlaceholders(s)
		for _, name := range paramRefs {
			used[ParamName(name)] = struct{}{}
		}
	}
	scan(wf.SystemPrompt)
	for i := range wf.Tasks {
		// Shell tasks reference params via {{params.x}} in their command body
		// exactly like LLM tasks do in their prompt; one of the two is empty
		// per the parser's XOR check, so scanning both is safe.
		scan(wf.Tasks[i].Prompt)
		scan(wf.Tasks[i].Command)
	}
	for _, p := range wf.Params {
		if _, ok := used[p.Name]; !ok {
			return &UnusedParamError{Name: p.Name}
		}
	}
	return nil
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
	// Forward depends-on edges (u -> each task u depends on) so a DFS that
	// re-enters a gray node has found a true dependency cycle. This is the
	// OPPOSITE direction from Plan's reverse dependents-edges; the two are kept
	// separate on purpose, so no shared builder.
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
