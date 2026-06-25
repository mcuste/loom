package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mcuste/loom/pkg/runtime"
)

// Parse decodes a workflow YAML document and returns the validated Workflow.
//
// The decoder runs in known-fields mode: any top-level or task-level key not
// recognized by the current schema is rejected. The sub-workflow constructs
// (top-level output:, task-level workflow: and with:) are recognized here;
// linking the child workflows referenced by `workflow:` is a separate CLI step
// so this package stays filesystem-free.
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

	// Loops are declared as tasks carrying a `loop:` (while) or `for_each:` block:
	// the wrapper is not an executable task; its id becomes the loop id and its
	// nested `tasks:` the members. Split wrappers out of the top-level task set
	// and collect them as rawLoops for the shared loop-group machinery.
	var rawLoops []rawLoop
	topTasks := make([]rawTask, 0, len(raw.Tasks))
	for _, rt := range raw.Tasks {
		isLoop := rt.Loop.Kind != 0
		isForEach := rt.ForEach.Kind != 0
		if !isLoop && !isForEach {
			topTasks = append(topTasks, rt)
			continue
		}
		tid, err := NewTaskID(rt.ID)
		if err != nil {
			return nil, err
		}
		// `loop:` and `for_each:` are sibling scoped-block wrappers; a task
		// declaring both is ambiguous.
		if isLoop && isForEach {
			return nil, &TaskBodyConflictError{Task: tid, Fields: []string{"loop", "for_each"}}
		}
		wrapper := "loop"
		if isForEach {
			wrapper = "for_each"
		}
		if err := rejectLoopWrapperFields(tid, rt, wrapper); err != nil {
			return nil, err
		}
		rl := rawLoop{id: LoopID(tid), description: rt.Description}
		if isForEach {
			rl.kind = LoopForEach
			if err := decodeForEachBody(&rt.ForEach, &rl); err != nil {
				return nil, fmt.Errorf("task %q: %w", tid, err)
			}
		} else if err := decodeLoopBody(&rt.Loop, &rl); err != nil {
			return nil, fmt.Errorf("task %q: %w", tid, err)
		}
		rawLoops = append(rawLoops, rl)
	}

	// A zero top-level task set is rejected, but the sentinel depends on whether
	// any loops are declared: an empty workflow gets ErrNoTasks, while a
	// loops-only workflow gets the clearer ErrLoopsWithoutTopLevelTask.
	if len(topTasks) == 0 {
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
	for _, rt := range topTasks {
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

	loops, memberByLoop, err := buildLoopGroups(rawLoops, depsByID, ids, paramSet)
	if err != nil {
		return nil, err
	}
	wf.Loops = loops

	// asByLoop maps each loop id to its for_each loop variable ("" for a while
	// loop), so the per-task build below can exempt a member's `{{as}}`
	// placeholder from the depends_on check (it is bound per iteration, not via
	// the DAG).
	asByLoop := make(map[LoopID]string, len(loops))
	for i := range loops {
		asByLoop[loops[i].ID] = loops[i].As
	}

	for _, lt := range allTasks {
		rt := lt.rt
		tid := TaskID(rt.ID)

		// Exactly one body form. loop/for_each wrappers were split out above, so
		// the only forms that can appear here are prompt, prompt_file, command,
		// and workflow; `workflow:` conflicts with each of the others.
		var bodyForms []string
		if rt.Prompt != "" {
			bodyForms = append(bodyForms, "prompt")
		}
		if rt.PromptFile != "" {
			bodyForms = append(bodyForms, "prompt_file")
		}
		if rt.Command != "" {
			bodyForms = append(bodyForms, "command")
		}
		if rt.Workflow != "" {
			bodyForms = append(bodyForms, "workflow")
		}
		switch {
		case len(bodyForms) > 1:
			return nil, &TaskBodyConflictError{Task: tid, Fields: bodyForms}
		case len(bodyForms) == 0:
			return nil, fmt.Errorf("task %q: %w", tid, ErrMissingPromptOrCommand)
		}
		// `with:` is only meaningful alongside `workflow:`.
		if rt.With.Kind != 0 && rt.Workflow == "" {
			return nil, fmt.Errorf("task %q: with: is only valid on a workflow task", tid)
		}

		// body is the text that placeholder validation runs against;
		// substitution targets the same string at execution time. A sub-workflow
		// task has no prompt body: its placeholder-derived deps come from scanning
		// its with-values instead.
		body := rt.Prompt
		var withArgs []WithArg
		switch {
		case rt.Command != "":
			body = rt.Command
			if rt.Runtime != "" || rt.Model != "" || rt.Effort != "" {
				return nil, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithRuntime)
			}
		case rt.Workflow != "":
			if rt.Runtime != "" || rt.Model != "" || rt.Effort != "" {
				return nil, fmt.Errorf("task %q: %w", tid, ErrSubWorkflowWithRuntime)
			}
			wa, werr := decodeWith(tid, rt.With)
			if werr != nil {
				return nil, werr
			}
			withArgs = wa
			// Join the with-values so the malformed-placeholder check below scans
			// every value the way it scans a prompt body.
			var sb strings.Builder
			for _, a := range withArgs {
				sb.WriteString(a.Value)
				sb.WriteByte('\n')
			}
			body = sb.String()
		case rt.PromptFile != "":
			// A prompt_file must be inlined by InlinePromptFiles before Parse; one
			// reaching here was not, so there is no body to build a task from.
			return nil, fmt.Errorf("task %q: prompt_file must be inlined before parsing", tid)
		}
		schema, err := parseSchema(rt.Schema)
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", tid, err)
		}
		if schema != nil && rt.Command != "" {
			return nil, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithSchema)
		}
		var deps []TaskID
		if rt.Workflow != "" {
			deps, err = buildSubWorkflowDeps(tid, rt.DependsOn, withArgs, ids, paramSet, asByLoop[lt.loop])
		} else {
			deps, err = buildDeps(tid, rt.DependsOn, body, ids, paramSet, asByLoop[lt.loop])
		}
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
			ID:          tid,
			Prompt:      rt.Prompt,
			Command:     rt.Command,
			Description: rt.Description,
			Runtime:     runtime.Name(rt.Runtime),
			Model:       runtime.Model(rt.Model),
			Effort:      runtime.Effort(rt.Effort),
			DependsOn:   deps,
			When:        rt.When,
			Cond:        cond,
			Retry:       retry,
			WritesState: rt.WritesState,
			Budget:      taskBudget,
			Schema:      schema,
			Cache:       rt.Cache,
			Loop:        lt.loop,
			Workflow:    rt.Workflow,
			With:        withArgs,
		})
	}

	if raw.Output != "" {
		ot := TaskID(raw.Output)
		if _, ok := wf.byID[ot]; !ok {
			return nil, &UnknownOutputTaskError{Task: ot}
		}
		wf.Output = ot
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

	budget, err := parseBudget(raw.Budget)
	if err != nil {
		return nil, err
	}
	wf.Budget = budget

	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if t.IsShell() || t.IsSubWorkflow() {
			// Shell and sub-workflow tasks bypass the runtime registry entirely;
			// runtime/model/effort have no meaning (the child brings its own), and
			// the task-level reject above guarantees they are unset on t.
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
	// Inline any `prompt_file:` references relative to the YAML's own directory
	// before Parse, so Parse only ever sees inline `prompt:` bodies.
	inlined, err := InlinePromptFiles(data, filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	wf, err := Parse(inlined)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return wf, nil
}

// InlinePromptFiles rewrites file-reference keys into their inline equivalents
// by reading the referenced files relative to baseDir:
//
//   - every task's `prompt_file:` becomes an inline `prompt:`, and
//   - a top-level `system_prompt_file:` becomes an inline `system_prompt:`.
//
// For task prompts it walks the YAML node tree and, for each mapping node that
// carries a `prompt_file` key, enforces the 5-way body-form mutual exclusivity
// (a task sets at most one of prompt, prompt_file, command, loop, for_each),
// rejects absolute paths, reads the file at filepath.Join(baseDir, path), and
// replaces the `prompt_file` key+value with a `prompt` whose literal-block value
// is the file content. The walk covers nested loop bodies, so a `prompt_file`
// inside a `loop:` or `for_each:` body is inlined the same way.
//
// system_prompt is a workflow-level field, so `system_prompt_file` is inlined
// only on the document's root mapping; one nested in a task is left for Parse's
// known-fields check to reject. Setting both `system_prompt` and
// `system_prompt_file` is rejected with ErrSystemPromptAndFileSet.
//
// The rewritten bytes are self-contained: Parse never sees either *_file key, so
// KnownFields(true) strictness is preserved. InlinePromptFiles short-circuits
// with no YAML round-trip when the raw bytes contain no `prompt_file` token
// (which also covers `system_prompt_file`, since it contains that substring).
func InlinePromptFiles(data []byte, baseDir string) ([]byte, error) {
	// Fast path: the overwhelming majority of workflows carry no `prompt_file`
	// key, so skip the unmarshal + node walk + marshal round-trip entirely when
	// the token is absent from the raw bytes. `system_prompt_file` contains the
	// `prompt_file` substring, so this guard catches it too.
	if !bytes.Contains(data, []byte("prompt_file")) {
		return data, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if err := inlineSystemPromptFile(&doc, baseDir); err != nil {
		return nil, err
	}
	if err := inlinePromptFileNodes(&doc, baseDir); err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return out, nil
}

// inlineSystemPromptFile rewrites a top-level `system_prompt_file:` key into an
// inline `system_prompt:` by reading the referenced file relative to baseDir.
//
// Only the document's root mapping is considered: system_prompt is a
// workflow-level field, so a `system_prompt_file` nested in a task mapping is
// left untouched for Parse's known-fields check to reject in context. A workflow
// that sets both `system_prompt` and `system_prompt_file` is rejected with
// ErrSystemPromptAndFileSet. The rules otherwise mirror task prompt_file:
// absolute paths are rejected, and a read failure surfaces as a
// SystemPromptFileError wrapping the OS error.
func inlineSystemPromptFile(doc *yaml.Node, baseDir string) error {
	root := doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	var keyNode, value *yaml.Node
	hasInline := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		switch k.Value {
		case "system_prompt_file":
			keyNode, value = k, v
		case "system_prompt":
			hasInline = true
		}
	}
	if keyNode == nil {
		return nil
	}
	if hasInline {
		return ErrSystemPromptAndFileSet
	}
	if filepath.IsAbs(value.Value) {
		return &AbsoluteSystemPromptFilePathError{Path: value.Value}
	}
	content, err := os.ReadFile(filepath.Join(baseDir, value.Value))
	if err != nil {
		return &SystemPromptFileError{Path: value.Value, Err: err}
	}
	keyNode.Value = "system_prompt"
	value.Kind = yaml.ScalarNode
	value.Tag = "!!str"
	value.Style = yaml.LiteralStyle
	value.Value = string(content)
	return nil
}

// inlinePromptFileNodes recurses the node tree, inlining `prompt_file` in every
// mapping it visits. Walking the whole tree (not just top-level tasks) means a
// `prompt_file` inside a nested loop body is inlined the same way.
func inlinePromptFileNodes(n *yaml.Node, baseDir string) error {
	if n.Kind == yaml.MappingNode {
		if err := inlinePromptFileMapping(n, baseDir); err != nil {
			return err
		}
		// A mapping's Content alternates key/value; keys are scalars that can
		// never hold a nested body, so recurse into the value nodes only.
		for i := 1; i < len(n.Content); i += 2 {
			if err := inlinePromptFileNodes(n.Content[i], baseDir); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range n.Content {
		if err := inlinePromptFileNodes(c, baseDir); err != nil {
			return err
		}
	}
	return nil
}

// inlinePromptFileMapping inlines a single mapping's `prompt_file` key in place.
// A mapping without `prompt_file` is left untouched.
func inlinePromptFileMapping(m *yaml.Node, baseDir string) error {
	var (
		taskID         TaskID
		keyNode, value *yaml.Node
		bodyFields     []string
		hasID          bool
	)
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		switch k.Value {
		case "id":
			taskID = TaskID(v.Value)
			hasID = true
		case "prompt_file":
			keyNode, value = k, v
			bodyFields = append(bodyFields, k.Value)
		case "prompt", "command", "loop", "for_each", "workflow":
			bodyFields = append(bodyFields, k.Value)
		}
	}
	if keyNode == nil {
		return nil
	}
	// Only task mappings legitimately carry `prompt_file`, and every task has an
	// `id`. A stray `prompt_file` in any other mapping (schema body, loop block,
	// workflow root) is left untouched for Parse's known-fields check to reject
	// in context, rather than inlined or reported against an empty task id.
	if !hasID {
		return nil
	}
	// prompt_file is one of the mutually exclusive body forms: any sibling body
	// key is a conflict, reported with every offending field in declaration order.
	if len(bodyFields) > 1 {
		return &TaskBodyConflictError{Task: taskID, Fields: bodyFields}
	}
	if filepath.IsAbs(value.Value) {
		return &AbsolutePromptFilePathError{Task: taskID, Path: value.Value}
	}
	content, err := os.ReadFile(filepath.Join(baseDir, value.Value))
	if err != nil {
		return &PromptFileError{Task: taskID, Path: value.Value, Err: err}
	}
	keyNode.Value = "prompt"
	value.Kind = yaml.ScalarNode
	value.Tag = "!!str"
	value.Style = yaml.LiteralStyle
	value.Value = string(content)
	return nil
}

// AbsolutePromptFilePathError reports a `prompt_file:` whose value is an
// absolute path. Only paths relative to the workflow file's own directory are
// permitted; this keeps workflows self-contained and shareable.
type AbsolutePromptFilePathError struct {
	Task TaskID
	Path string
}

func (e *AbsolutePromptFilePathError) Error() string {
	return fmt.Sprintf("task %q: prompt_file %q must be a relative path", e.Task, e.Path)
}

// PromptFileError reports a `prompt_file:` that could not be read (file missing,
// permission denied, or any other I/O failure). Err wraps the underlying OS
// error so errors.Is(err, os.ErrNotExist) works for callers that need to
// distinguish "file not found" from other failures.
type PromptFileError struct {
	Task TaskID
	Path string
	Err  error
}

func (e *PromptFileError) Error() string {
	return fmt.Sprintf("task %q: read prompt_file %q: %v", e.Task, e.Path, e.Err)
}

func (e *PromptFileError) Unwrap() error { return e.Err }

// AbsoluteSystemPromptFilePathError reports a top-level `system_prompt_file:`
// whose value is an absolute path. Only paths relative to the workflow file's
// own directory are permitted, matching the task-level prompt_file rule.
type AbsoluteSystemPromptFilePathError struct {
	Path string
}

func (e *AbsoluteSystemPromptFilePathError) Error() string {
	return fmt.Sprintf("system_prompt_file %q must be a relative path", e.Path)
}

// SystemPromptFileError reports a top-level `system_prompt_file:` that could not
// be read (file missing, permission denied, or any other I/O failure). Err
// wraps the underlying OS error so errors.Is(err, os.ErrNotExist) works.
type SystemPromptFileError struct {
	Path string
	Err  error
}

func (e *SystemPromptFileError) Error() string {
	return fmt.Sprintf("read system_prompt_file %q: %v", e.Path, e.Err)
}

func (e *SystemPromptFileError) Unwrap() error { return e.Err }

// TaskBodyConflictError reports a task that sets more than one of the five
// mutually exclusive body forms: prompt, prompt_file, command, loop, for_each.
// Fields lists every conflicting key in the order they appear in the YAML
// document, so the error message is deterministic and points directly at the
// offending lines.
//
// TaskBodyConflictError unifies the legacy pairwise sentinels: its Is method
// returns true for ErrPromptAndCommandSet, ErrLoopTaskWithBody, and
// ErrLoopAndForEachSet when Fields contains the corresponding combination, so
// callers using errors.Is against those sentinel values continue to work.
type TaskBodyConflictError struct {
	Task   TaskID
	Fields []string
}

func (e *TaskBodyConflictError) Error() string {
	return fmt.Sprintf("task %q sets mutually exclusive fields: %s", e.Task, strings.Join(e.Fields, ", "))
}

func (e *TaskBodyConflictError) Is(target error) bool {
	has := func(f string) bool { return slices.Contains(e.Fields, f) }
	switch target {
	case ErrPromptAndCommandSet:
		return has("prompt") && has("command")
	case ErrLoopAndForEachSet:
		return has("loop") && has("for_each")
	case ErrLoopTaskWithBody:
		return (has("loop") || has("for_each")) && (has("prompt") || has("command") || has("prompt_file"))
	}
	return false
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
	// There is no top-level `loops:` key: loops are declared inline as tasks
	// carrying a `loop:` block (see rawTask.Loop). With KnownFields(true), a
	// stray top-level `loops:` is rejected as an unknown field.
	//
	// Budget is captured as a raw yaml.Node so the parser can distinguish an
	// absent `budget:` key (no limit) from a present block whose max_cost_usd
	// must be validated as a positive float.
	Budget yaml.Node `yaml:"budget"`
	// Cache is the workflow-level memoization default. A plain bool suffices: an
	// absent `cache:` key decodes to false, which is exactly the "off unless a
	// task opts in" default.
	Cache bool `yaml:"cache"`
	// Output names the task whose output is this workflow's result string when it
	// is run as a sub-workflow. Empty selects the lone sink by default; see
	// Workflow.OutputTask. Validated to name a known task.
	Output string `yaml:"output"`
}

// rawTask mirrors the per-task YAML schema. It exists so the parser can apply
// its own validation before promoting values to the typed Task. Several fields
// are yaml.Node to let the parser distinguish an absent key (zero value, inherit
// default) from a present-but-partial block that must be validated.
type rawTask struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	Runtime     string `yaml:"runtime"`
	Model       string `yaml:"model"`
	Effort      string `yaml:"effort"`
	Prompt      string `yaml:"prompt"`
	Command     string `yaml:"command"`
	// Workflow is the raw registry-name-or-path reference of a sub-workflow task.
	// A non-empty value makes this a sub-workflow leaf: the linked child is run
	// recursively at dispatch. Mutually exclusive with every other body form.
	Workflow string `yaml:"workflow"`
	// PromptFile is only present when Parse is handed YAML whose `prompt_file:`
	// was not inlined by InlinePromptFiles (e.g. a direct Parse call). It exists
	// so the body-form conflict check can see a `prompt_file` sibling; the normal
	// ParseFile path rewrites it to `prompt:` before Parse ever runs.
	PromptFile  string   `yaml:"prompt_file"`
	DependsOn   []string `yaml:"depends_on"`
	WritesState string   `yaml:"writes_state"`
	When        string   `yaml:"when"`
	// Retry is captured as a raw yaml.Node so the parser can distinguish an
	// absent `retry:` key (zero value, no retry) from a present-but-partial
	// block whose `backoff`/`on` defaults must be filled in.
	Retry yaml.Node `yaml:"retry"`
	// ForEach is captured as a raw yaml.Node so the parser can tell an absent
	// `for_each:` key (a normal prompt/command task) from a present block, which
	// makes this task a for_each wrapper: its id becomes the loop id and its
	// nested `tasks:` the loop body, decoded by decodeForEachBody. A sibling of
	// Loop; a task may set at most one of the two.
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
	// Loop is captured as a raw yaml.Node so the parser can tell an absent
	// `loop:` key (a normal prompt/command task) from a present block, which
	// makes this task a loop wrapper: its id becomes the loop id and its nested
	// `tasks:` the loop body, decoded by decodeLoopBody.
	Loop yaml.Node `yaml:"loop"`
	// With is captured as a raw yaml.Node so the parser can preserve declaration
	// order and validate each key as an identifier when decoding it into the
	// ordered []WithArg. Only meaningful on a sub-workflow task.
	With yaml.Node `yaml:"with"`
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
	// ErrMissingParamName is returned when a params entry omits the name field.
	ErrMissingParamName = errors.New("param has no name")

	// ErrMissingPromptOrCommand reports a task that sets none of the body forms
	// (`prompt:`, `prompt_file:`, `command:`, `loop:`, `for_each:`, or
	// `workflow:`). Exactly one must be present.
	ErrMissingPromptOrCommand = errors.New("task has no prompt, prompt_file, command, loop, for_each, or workflow")
	// ErrPromptAndCommandSet reports a task that sets both `prompt:` and
	// `command:`. The two are mutually exclusive.
	ErrPromptAndCommandSet = errors.New("task sets both prompt and command")
	// ErrSystemPromptAndFileSet reports a workflow that sets both the inline
	// `system_prompt:` and `system_prompt_file:`. The two are mutually exclusive:
	// system_prompt_file is just the file-backed spelling of system_prompt.
	ErrSystemPromptAndFileSet = errors.New("workflow sets both system_prompt and system_prompt_file")
	// ErrShellTaskWithRuntime reports a shell task (one with `command:`) that
	// also sets task-level `runtime:`, `model:`, or `effort:`. These fields are
	// meaningless for shell tasks and rejected at the task level; workflow-level
	// defaults are tolerated.
	ErrShellTaskWithRuntime = errors.New("shell task must not set runtime, model, or effort")
	// ErrLoopTaskWithBody reports a loop-wrapper task (one with a `loop:` block)
	// that also sets a body form (`prompt:`, `prompt_file:`, or `command:`). A
	// loop task has no body of its own; its work lives in the nested loop tasks.
	ErrLoopTaskWithBody = errors.New("loop task must not set prompt, prompt_file, or command")
	// ErrLoopTaskWithRuntime reports a loop-wrapper task that sets task-level
	// `runtime:`, `model:`, or `effort:`. These belong to the loop's body tasks,
	// not the wrapper.
	ErrLoopTaskWithRuntime = errors.New("loop task must not set runtime, model, or effort")
	// ErrLoopTaskWithFields reports a loop-wrapper task that sets a task-only
	// field (`depends_on`, `when`, `writes_state`, `schema`, `retry`, `budget`,
	// or `cache`). The wrapper stands only for its loop; entry dependencies and
	// per-task behavior belong to the body tasks.
	ErrLoopTaskWithFields = errors.New("loop task must not set depends_on, when, writes_state, schema, retry, budget, or cache")
	// ErrLoopAndForEachSet reports a task declaring both a `loop:` and a
	// `for_each:` block. The two are sibling scoped-block forms; a task is at
	// most one of them.
	ErrLoopAndForEachSet = errors.New("task sets both loop and for_each")
	// ErrSubWorkflowWithRuntime reports a sub-workflow task (one with `workflow:`)
	// that also sets task-level `runtime:`, `model:`, or `effort:`. The linked
	// child brings its own runtime; these knobs are meaningless on the wrapper,
	// exactly as for a shell task.
	ErrSubWorkflowWithRuntime = errors.New("sub-workflow task must not set runtime, model, or effort")
)

// rejectLoopWrapperFields enforces that a `loop:` task carries nothing but its
// id, description, and the loop block: a loop wrapper is not an executable task,
// so prompt/command, runtime knobs, and every task-only field are rejected at
// load time rather than silently ignored.
func rejectLoopWrapperFields(tid TaskID, rt rawTask, wrapper string) error {
	switch {
	case rt.Prompt != "" || rt.Command != "" || rt.Workflow != "":
		body := "prompt"
		switch {
		case rt.Command != "":
			body = "command"
		case rt.Workflow != "":
			body = "workflow"
		}
		return &TaskBodyConflictError{Task: tid, Fields: []string{wrapper, body}}
	case rt.Runtime != "" || rt.Model != "" || rt.Effort != "":
		return fmt.Errorf("task %q: %w", tid, ErrLoopTaskWithRuntime)
	case len(rt.DependsOn) > 0 || rt.When != "" || rt.WritesState != "" ||
		rt.Schema.Kind != 0 || rt.Retry.Kind != 0 ||
		rt.Budget.Kind != 0 || rt.Cache != nil:
		return fmt.Errorf("task %q: %w", tid, ErrLoopTaskWithFields)
	}
	return nil
}

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

// decodeWith decodes a sub-workflow task's `with:` mapping into ordered
// WithArg entries. An absent block (zero-value node) yields nil. Each key must
// satisfy the identifier alphabet (it names a child param); each value is taken
// as its literal scalar text, substituted with the parent context at run time.
func decodeWith(tid TaskID, node yaml.Node) ([]WithArg, error) {
	if node.Kind == 0 {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("task %q: with must be a mapping", tid)
	}
	args := make([]WithArg, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("task %q: with key must be a scalar", tid)
		}
		name, err := NewParamName(k.Value)
		if err != nil {
			return nil, fmt.Errorf("task %q: with: %w", tid, err)
		}
		var val string
		if err := v.Decode(&val); err != nil {
			return nil, fmt.Errorf("task %q: with.%s: %w", tid, name, err)
		}
		args = append(args, WithArg{Name: name, Value: val})
	}
	return args, nil
}

// buildSubWorkflowDeps computes a sub-workflow task's dependency list. Unlike a
// prompt body (where a `{{x}}` placeholder must already be declared in
// depends_on), a task-id placeholder in a with-VALUE extends the graph: it adds
// the edge implicitly, unioned with any explicit depends_on. Param placeholders
// in with-values are validated against the declared params; an unknown task or
// param reference is rejected the same way buildDeps rejects them. loopVar is
// the owning for_each's `as` variable ("" outside a for_each body): a with-value
// may reference it like any body member's prompt, so it is neither an edge nor
// an unknown ref.
func buildSubWorkflowDeps(tid TaskID, declared []string, withArgs []WithArg, known map[TaskID]struct{}, params map[ParamName]struct{}, loopVar string) ([]TaskID, error) {
	deps := make([]TaskID, 0, len(declared))
	seen := make(map[TaskID]struct{}, len(declared))

	for _, raw := range declared {
		d, err := NewTaskID(raw)
		if err != nil {
			return nil, fmt.Errorf("task %q depends_on: %w", tid, err)
		}
		if _, ok := known[d]; !ok {
			return nil, &UnknownDependencyError{Task: tid, Dep: d}
		}
		if _, dup := seen[d]; dup {
			return nil, &DuplicateDependencyError{Task: tid, Dep: d}
		}
		seen[d] = struct{}{}
		deps = append(deps, d)
	}

	for _, a := range withArgs {
		taskRefs, paramRefs, _ := scanPlaceholders(a.Value)
		for _, name := range taskRefs {
			if name == loopVar {
				continue
			}
			id := TaskID(name)
			if _, ok := known[id]; !ok {
				return nil, &UnknownPlaceholderError{Task: tid, Name: name}
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			deps = append(deps, id)
		}
		for _, name := range paramRefs {
			if _, ok := params[ParamName(name)]; !ok {
				return nil, &UnknownParamError{Task: tid, Name: name}
			}
		}
	}
	return deps, nil
}

// scanPlaceholders walks text in a SINGLE pass with combinedPlaceholderRe and
// returns the task-id, param, and state placeholder names in source order. The
// combined regex disambiguates `{{params.x}}` (group 1), `{{state.x}}` (group
// 2), `{{prev.x}}` (group 3), and `{{id}}` (group 4), so the slices never
// cross-contaminate. State refs are returned separately so callers can treat
// them as neither task edges nor param references: they need no declaration and
// create no DAG edge. Prev refs are skipped here: they reference a prior
// iteration's output, so they create no DAG edge and are validated separately.
func scanPlaceholders(text string) (taskRefs, paramRefs, stateRefs []string) {
	for _, m := range combinedPlaceholderRe.FindAllStringSubmatch(text, -1) {
		// Exactly one capture group is non-empty per match: group 1 is the param
		// name, group 2 is the state key, group 3 is the prev id, group 4 is the
		// bare task id.
		switch {
		case m[1] != "":
			paramRefs = append(paramRefs, m[1])
		case m[2] != "":
			stateRefs = append(stateRefs, m[2])
		case m[3] != "":
			// prev ref: neither a task edge nor a param reference.
		default:
			taskRefs = append(taskRefs, m[4])
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
		// A sub-workflow task carries its param references in its with-values
		// rather than a prompt body; scan those so a param used only there is
		// still counted as referenced.
		for _, a := range wf.Tasks[i].With {
			scan(a.Value)
		}
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
