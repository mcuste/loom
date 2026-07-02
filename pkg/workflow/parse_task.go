package workflow

import (
	"fmt"
	"strings"

	"github.com/mcuste/loom/pkg/runtime"
)

// loopTask pairs a raw task with the id of its owning loop. The loop field is
// "" for top-level (non-loop-member) tasks.
type loopTask struct {
	rt   rawTask
	loop LoopID
}

// parseState bundles the mutable state threaded through the per-task build
// loop: the workflow being assembled, the global task-id set used for
// dependency and placeholder validation, the param name set, and the per-loop
// metadata derived from the raw loop declarations.
type parseState struct {
	wf           *Workflow
	ids          map[TaskID]struct{}
	paramSet     map[ParamName]struct{}
	asByLoop     map[LoopID]string
	memberByLoop map[LoopID]map[TaskID]bool
}

type taskMeta struct {
	schema *Schema
	deps   []TaskID
	cond   *Condition
	retry  Retry
	budget *Budget
}

// buildTask constructs one Task from a raw task (paired with its owning loop
// id, "" for top-level tasks) and appends it to st.wf. It performs all
// per-task validation: body-form checks, dependency graph edges, placeholder
// validation, schema, when-expression, etc.
func buildTask(st *parseState, lt loopTask) error {
	rt := lt.rt
	tid := TaskID(rt.ID)
	wf := st.wf

	// Exactly one body form. loop/for_each wrappers were split out above, so
	// the only forms that can appear here are prompt, prompt_file, command,
	// workflow, and script; each conflicts with all the others.
	presentForms := detectBodyForms(rt)
	switch {
	case len(presentForms) > 1:
		return &TaskBodyConflictError{Task: tid, Fields: presentForms}
	case len(presentForms) == 0:
		return fmt.Errorf("task %q: %w", tid, ErrMissingPromptOrCommand)
	}
	// with: is only meaningful alongside workflow:.
	if rt.With.Kind != 0 && rt.Workflow == "" {
		return fmt.Errorf("task %q: with: is only valid on a workflow task", tid)
	}
	// args: is only meaningful alongside script:.
	if len(rt.Args) > 0 && rt.Script == "" {
		return fmt.Errorf("task %q: %w", tid, ErrArgsWithoutScript)
	}
	if err := validateOkExit(tid, rt); err != nil {
		return err
	}

	body, withArgs, err := normalizeTaskBody(tid, rt)
	if err != nil {
		return err
	}
	meta, err := buildTaskMeta(st, lt.loop, tid, rt, body, withArgs)
	if err != nil {
		return err
	}
	loopVar := st.asByLoop[lt.loop]
	action := taskActionFromRaw(rt, withArgs, loopVar)
	wf.byID[tid] = len(wf.Tasks)
	wf.Tasks = append(wf.Tasks, Task{
		ID:                   tid,
		Prompt:               rt.Prompt,
		Command:              rt.Command,
		Description:          rt.Description,
		Runtime:              runtime.Name(rt.Runtime),
		Model:                runtime.Model(rt.Model),
		Effort:               runtime.Effort(rt.Effort),
		SystemPrompt:         rt.SystemPrompt,
		systemPromptTemplate: ParseTemplate(rt.SystemPrompt),
		DependsOn:            meta.deps,
		When:                 rt.When,
		Cond:                 meta.cond,
		Retry:                meta.retry,
		WritesState:          rt.WritesState,
		Budget:               meta.budget,
		Schema:               meta.schema,
		Cache:                rt.Cache,
		Loop:                 lt.loop,
		Workflow:             rt.Workflow,
		With:                 withArgs,
		Script:               rt.Script,
		Args:                 rt.Args,
		OkExit:               rt.OkExit,
		action:               action,
	})
	return nil
}

func validateOkExit(tid TaskID, rt rawTask) error {
	if len(rt.OkExit) == 0 {
		return nil
	}
	if rt.Workflow != "" {
		return fmt.Errorf("task %q: %w", tid, ErrOkExitOnSubWorkflow)
	}
	for _, code := range rt.OkExit {
		if code < 0 || code > 255 {
			return fmt.Errorf("task %q: %w: %d", tid, ErrOkExitOutOfRange, code)
		}
	}
	return nil
}

// normalizeTaskBody picks the single body text placeholder validation runs
// against and decodes any sub-workflow with: arguments.
func normalizeTaskBody(tid TaskID, rt rawTask) (string, []WithArg, error) {
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
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithRuntime)
		}
		if rt.SystemPrompt != "" || rt.SystemPromptFile != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithSystemPrompt)
		}
	case rt.Workflow != "":
		// runtime/model/effort on a sub-workflow task are not rejected: they
		// override the linked child's workflow-level defaults at link time (see
		// linkSubWorkflows), so a parent can run a shared child cheaper without
		// forking it. system_prompt stays rejected: the child's tasks carry their
		// own, and there is no single task here for one to apply to.
		if rt.SystemPrompt != "" || rt.SystemPromptFile != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrSubWorkflowWithSystemPrompt)
		}
		wa, err := decodeWith(tid, rt.With)
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
	case rt.Script != "":
		if rt.Runtime != "" || rt.Model != "" || rt.Effort != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrScriptTaskWithRuntime)
		}
		if rt.SystemPrompt != "" || rt.SystemPromptFile != "" {
			return "", nil, fmt.Errorf("task %q: %w", tid, ErrScriptTaskWithSystemPrompt)
		}
		// The script path and its argv carry placeholders, so scan all of them:
		// each {{id}} / {{id.exit}} ref must resolve to a dependency.
		var sb strings.Builder
		sb.WriteString(rt.Script)
		for _, a := range rt.Args {
			sb.WriteByte('\n')
			sb.WriteString(a)
		}
		body = sb.String()
	case rt.PromptFile != "":
		// A prompt_file must be inlined by InlinePromptFiles before Parse; one
		// reaching here was not, so there is no body to build a task from.
		return "", nil, fmt.Errorf("task %q: prompt_file must be inlined before parsing", tid)
	}
	return body, withArgs, nil
}

func buildTaskMeta(st *parseState, loop LoopID, tid TaskID, rt rawTask, body string, withArgs []WithArg) (taskMeta, error) {
	schema, err := parseSchema(rt.Schema)
	if err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	if schema != nil && rt.Command != "" {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, ErrShellTaskWithSchema)
	}
	if schema != nil && rt.Script != "" {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, ErrScriptTaskWithSchema)
	}
	if err := validateRoutingField(tid, "runtime", rt.Runtime, st.paramSet); err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	if err := validateRoutingField(tid, "model", rt.Model, st.paramSet); err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	if err := validateRoutingField(tid, "effort", rt.Effort, st.paramSet); err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	dc := depsCtx{tid: tid, known: st.ids, params: st.paramSet, loopVar: st.asByLoop[loop]}
	var deps []TaskID
	if rt.Workflow != "" {
		deps, err = buildSubWorkflowDeps(dc, rt.DependsOn, withArgs)
	} else {
		deps, err = buildDeps(dc, rt.DependsOn, body)
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
	if rt.SystemPromptFile != "" {
		return taskMeta{}, fmt.Errorf("task %q: system_prompt_file must be inlined before parsing", tid)
	}
	// A task-level system prompt is validated exactly like the workflow-level
	// one: declared param placeholders only, no task-id placeholders. Shell and
	// sub-workflow tasks were already rejected above, so rt.SystemPrompt here
	// belongs to an LLM task.
	if err := validateSystemPrompt(rt.SystemPrompt, st.paramSet); err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	retry, err := parseRetry(tid, rt.Retry)
	if err != nil {
		return taskMeta{}, err
	}
	if rt.WritesState != "" && !identifierRe.MatchString(rt.WritesState) {
		return taskMeta{}, &InvalidWritesStateError{Task: tid, Key: rt.WritesState}
	}
	taskBudget, err := parseBudget(rt.Budget)
	if err != nil {
		return taskMeta{}, fmt.Errorf("task %q: %w", tid, err)
	}
	// A when: expression may only reference this task's dependencies: the
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
