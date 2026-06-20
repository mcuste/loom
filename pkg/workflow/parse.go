package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
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
	if len(raw.Tasks) == 0 {
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
		deps, err := buildDeps(tid, rt.DependsOn, body, ids, paramSet)
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
		wf.byID[tid] = len(wf.Tasks)
		wf.Tasks = append(wf.Tasks, Task{
			ID:          tid,
			Prompt:      rt.Prompt,
			Command:     rt.Command,
			Description: rt.Description,
			Runtime:     runtime.Name(rt.Runtime),
			Model:       runtime.Model(rt.Model),
			Effort:      runtime.Effort(rt.Effort),
			DependsOn:   deps,
			Retry:       retry,
		})
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
}

type rawTask struct {
	ID          string   `yaml:"id"`
	Description string   `yaml:"description"`
	Runtime     string   `yaml:"runtime"`
	Model       string   `yaml:"model"`
	Effort      string   `yaml:"effort"`
	Prompt      string   `yaml:"prompt"`
	Command     string   `yaml:"command"`
	DependsOn   []string `yaml:"depends_on"`
	// Retry is captured as a raw yaml.Node so the parser can distinguish an
	// absent `retry:` key (zero value, no retry) from a present-but-partial
	// block whose `backoff`/`on` defaults must be filled in.
	Retry yaml.Node `yaml:"retry"`
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
func buildDeps(tid TaskID, declared []string, prompt string, known map[TaskID]struct{}, params map[ParamName]struct{}) ([]TaskID, error) {
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
		name := m[1]
		if _, ok := declaredSet[TaskID(name)]; ok {
			continue
		}
		err := &UnknownPlaceholderError{Task: tid, Name: name}
		if _, isParam := params[ParamName(name)]; isParam {
			err.Hint = fmt.Sprintf("did you mean {{params.%s}}?", name)
		}
		return nil, err
	}

	for _, m := range paramPlaceholderRe.FindAllStringSubmatch(prompt, -1) {
		if _, ok := params[ParamName(m[1])]; !ok {
			return nil, &UnknownParamError{Task: tid, Name: m[1]}
		}
	}
	return deps, nil
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
	// Task-id placeholders are never resolvable in system_prompt.
	for _, m := range placeholderRe.FindAllStringSubmatch(sp, -1) {
		return &SystemPlaceholderTaskRefError{Name: m[1]}
	}
	for _, m := range paramPlaceholderRe.FindAllStringSubmatch(sp, -1) {
		if _, ok := params[ParamName(m[1])]; !ok {
			return &UnknownParamError{Task: "", Name: m[1]}
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
		for _, m := range paramPlaceholderRe.FindAllStringSubmatch(s, -1) {
			used[ParamName(m[1])] = struct{}{}
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
