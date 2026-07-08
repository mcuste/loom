package workflow

import (
	"fmt"
	"strings"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/syntax"
)

// taskDecl is the semantic task declaration lowered from syntax.DraftTask. It
// keeps YAML decoding details at the parser boundary while later task-building
// code works with validated identifiers and workflow-owned field types.
type taskDecl struct {
	id               TaskID
	loop             LoopID
	description      string
	runtime          runtime.Name
	model            runtime.Model
	effort           runtime.Effort
	body             taskBodyDecl
	systemPrompt     string
	systemPromptFile string
	okExit           []int
	dependsOn        []string
	writesState      string
	when             string
	retry            syntax.Value
	budget           syntax.Value
	schema           syntax.Value
	cache            *bool
	with             syntax.Value
}

func newTaskDecl(rt syntax.DraftTask, loop LoopID) (taskDecl, error) {
	id, err := NewTaskID(rt.ID)
	if err != nil {
		return taskDecl{}, err
	}
	return taskDecl{
		id:               id,
		loop:             loop,
		description:      rt.Description,
		runtime:          runtime.Name(rt.Runtime),
		model:            runtime.Model(rt.Model),
		effort:           runtime.Effort(rt.Effort),
		body:             newTaskBodyDecl(rt),
		systemPrompt:     rt.SystemPrompt,
		systemPromptFile: rt.SystemPromptFile,
		okExit:           append([]int(nil), rt.OkExit...),
		dependsOn:        append([]string(nil), rt.DependsOn...),
		writesState:      rt.WritesState,
		when:             rt.When,
		retry:            rt.Retry,
		budget:           rt.Budget,
		schema:           rt.Schema,
		cache:            rt.Cache,
		with:             rt.With,
	}, nil
}

// parseState bundles the immutable scope threaded through per-task lowering:
// the global task-id set used for dependency and placeholder validation, the
// param name set, and the per-loop metadata derived from the decoded loop
// declarations.
type parseState struct {
	ids      map[TaskID]struct{}
	paramSet map[ParamName]struct{}
	asByLoop map[LoopID]string
}

type taskMeta struct {
	schema *Schema
	deps   []TaskID
	cond   *Condition
	retry  Retry
	budget *Budget
}

// buildTaskNode lowers one task declaration into the semantic node model. It
// performs all per-task validation: body-form checks, dependency graph edges,
// placeholder validation, schema, when-expression, and executable action
// construction. Legacy Task values are materialized later from the completed
// WorkflowDefinition.
func buildTaskNode(st *parseState, rt taskDecl) (TaskNode, error) {
	tid := rt.id

	// Exactly one body form. loop/for_each wrappers were split out above, so
	// the only forms that can appear here are prompt, prompt_file, command,
	// workflow, and script; each conflicts with all the others.
	presentForms := detectBodyForms(rt)
	switch {
	case len(presentForms) > 1:
		return TaskNode{}, &TaskBodyConflictError{Task: tid, Fields: presentForms}
	case len(presentForms) == 0:
		return TaskNode{}, fmt.Errorf("task %q: %w", tid, ErrMissingPromptOrCommand)
	}
	// with: is only meaningful alongside workflow:.
	if rt.body.hasWith() && !rt.body.isSubWorkflow() {
		return TaskNode{}, fmt.Errorf("task %q: with: is only valid on a workflow task", tid)
	}
	// args: is only meaningful alongside script:.
	if rt.body.hasArgs() && !rt.body.isScript() {
		return TaskNode{}, fmt.Errorf("task %q: %w", tid, ErrArgsWithoutScript)
	}
	if err := validateOkExit(tid, rt); err != nil {
		return TaskNode{}, err
	}

	body, withArgs, err := normalizeTaskBody(tid, rt)
	if err != nil {
		return TaskNode{}, err
	}
	meta, err := buildTaskMeta(st, rt.loop, tid, rt, body, withArgs)
	if err != nil {
		return TaskNode{}, err
	}
	loopVar := st.asByLoop[rt.loop]
	return TaskNode{
		ID:          tid,
		Description: rt.description,
		DependsOn:   nodeIDs(meta.deps),
		Action:      taskActionFromDecl(rt, withArgs, loopVar),
		Condition:   meta.cond,
		When:        rt.when,
		Runtime: RuntimeSelector{
			Runtime: rt.runtime,
			Model:   rt.model,
			Effort:  rt.effort,
		},
		Policies: TaskPolicies{
			Retry:  meta.retry,
			Budget: meta.budget,
			Cache:  rt.cache,
			Schema: meta.schema,
			OkExit: append([]int(nil), rt.okExit...),
		},
		WritesState:  rt.writesState,
		Loop:         rt.loop,
		SystemPrompt: ParseTemplate(rt.systemPrompt),
	}, nil
}

func validateOkExit(tid TaskID, rt taskDecl) error {
	if len(rt.okExit) == 0 {
		return nil
	}
	if rt.body.isSubWorkflow() {
		return fmt.Errorf("task %q: %w", tid, ErrOkExitOnSubWorkflow)
	}
	for _, code := range rt.okExit {
		if code < 0 || code > 255 {
			return fmt.Errorf("task %q: %w: %d", tid, ErrOkExitOutOfRange, code)
		}
	}
	return nil
}

// normalizeTaskBody picks the single body text placeholder validation runs
// against and decodes any sub-workflow with: arguments.
func normalizeTaskBody(tid TaskID, rt taskDecl) (string, []WithArg, error) {
	// body is the text that placeholder validation runs against;
	// substitution targets the same string at execution time. A sub-workflow
	// task has no prompt body: its placeholder-derived deps come from scanning
	// its with-values instead.
	body := rt.body.prompt
	var withArgs []WithArg
	switch {
	case rt.body.isCommand():
		body = rt.body.command
		if rt.runtime != "" || rt.model != "" || rt.effort != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithRuntime)
		}
		if rt.systemPrompt != "" || rt.systemPromptFile != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithSystemPrompt)
		}
	case rt.body.isSubWorkflow():
		// runtime/model/effort on a sub-workflow task are not rejected: they
		// override the linked child's workflow-level defaults at link time (see
		// linkSubWorkflows), so a parent can run a shared child cheaper without
		// forking it. system_prompt stays rejected: the child's tasks carry their
		// own, and there is no single task here for one to apply to.
		if rt.systemPrompt != "" || rt.systemPromptFile != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrSubWorkflowWithSystemPrompt)
		}
		wa, err := decodeWith(tid, rt.body.with)
		if err != nil {
			return "", nil, err
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
	case rt.body.isScript():
		if rt.runtime != "" || rt.model != "" || rt.effort != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrScriptTaskWithRuntime)
		}
		if rt.systemPrompt != "" || rt.systemPromptFile != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrScriptTaskWithSystemPrompt)
		}
		// The script path and its argv carry placeholders, so scan all of them:
		// each {{id}} / {{id.exit}} ref must resolve to a dependency.
		var sb strings.Builder
		sb.WriteString(rt.body.script)
		for _, a := range rt.body.args {
			sb.WriteByte('\n')
			sb.WriteString(a)
		}
		body = sb.String()
	case rt.body.promptFile != "":
		// A prompt_file must be inlined by InlinePromptFiles before Parse; one
		// reaching here was not, so there is no body to build a task from.
		return "", nil, fmt.Errorf("task %q: prompt_file must be inlined before parsing", tid)
	}
	return body, withArgs, nil
}

func buildTaskMeta(st *parseState, loop LoopID, tid TaskID, rt taskDecl, body string, withArgs []WithArg) (taskMeta, error) {
	schema, err := parseSchema(rt.schema)
	if err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	if schema != nil && rt.body.isCommand() {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithSchema)
	}
	if schema != nil && rt.body.isScript() {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, ErrScriptTaskWithSchema)
	}
	if err := validateRoutingField(tid, "runtime", string(rt.runtime), st.paramSet); err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	if err := validateRoutingField(tid, "model", string(rt.model), st.paramSet); err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	if err := validateRoutingField(tid, "effort", string(rt.effort), st.paramSet); err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	dc := depsCtx{tid: tid, known: st.ids, params: st.paramSet, loopVar: st.asByLoop[loop]}
	var deps []TaskID
	if rt.body.isSubWorkflow() {
		deps, err = buildSubWorkflowDeps(dc, rt.dependsOn, withArgs)
	} else {
		deps, err = buildDeps(dc, rt.dependsOn, body)
	}
	if err != nil {
		return taskMeta{}, err
	}
	if err := checkMalformedParamPlaceholders(tid, body); err != nil {
		return taskMeta{}, err
	}
	// A task-level system_prompt_file must be inlined by InlinePromptFiles
	// before Parse, mirroring prompt_file; one reaching here was not, so reject
	// it rather than silently dropping the file-backed override.
	if rt.systemPromptFile != "" {
		return taskMeta{}, fmt.Errorf("task %q: system_prompt_file must be inlined before parsing", tid)
	}
	// A task-level system prompt is validated exactly like the workflow-level
	// one: declared param placeholders only, no task-id placeholders. Shell and
	// sub-workflow tasks were already rejected above, so rt.SystemPrompt here
	// belongs to an LLM task.
	if err := validateSystemPrompt(rt.systemPrompt, st.paramSet); err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	retry, err := parseRetry(tid, rt.retry)
	if err != nil {
		return taskMeta{}, err
	}
	if rt.writesState != "" && !identifierRe.MatchString(rt.writesState) {
		return taskMeta{}, &InvalidWritesStateError{Task: tid, Key: rt.writesState}
	}
	taskBudget, err := parseBudget(rt.budget)
	if err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	// A when: expression may only reference this task's dependencies: the
	// executor evaluates it after the dependency gates close, so a reference
	// to any other task (or the task's own id) could read an output that has
	// not been written yet. Bounding ParseCondition by depSet rejects those
	// at load time.
	var cond *Condition
	if rt.when != "" {
		depSet := make(map[TaskID]bool, len(deps))
		for _, d := range deps {
			depSet[d] = true
		}
		cond, err = ParseCondition(rt.when, depSet)
		if err != nil {
			return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
		}
	}
	return taskMeta{
		schema: schema,
		deps:   deps,
		cond:   cond,
		retry:  retry,
		budget: taskBudget,
	}, nil
}
