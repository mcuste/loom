// Package workflow defines the domain types for loom workflow definitions.
// See workflow.go for the core types and workflow.go's package doc.
//
// Parse decodes a workflow YAML document and returns the validated Workflow.
//
// The decoder runs in known-fields mode: any top-level or task-level key not
// recognized by the current schema is rejected. The sub-workflow constructs
// (top-level output:, task-level workflow: and with:) are recognized here;
// linking the child workflows referenced by workflow: is a separate CLI step
// so this package stays filesystem-free.
//
// Validation pipeline, in order:
//
//  1. Workflow name and every task id satisfy [A-Za-z0-9_]+.
//  2. Task ids are unique.
//  3. Param block: names are valid, unique, required-vs-default is exclusive,
//     defaults are scalar strings.
//  4. Every task sets exactly one of prompt: or command:. A task with
//     command: (a shell task) must not set task-level runtime, model, or
//     effort.
//  5. Every depends_on entry names a known task and appears at most once.
//  6. Every {{id}} placeholder in a prompt or command is a member of that
//     task's depends_on. Placeholders are pure templating; they never extend
//     the dependency graph implicitly.
//  7. Every {{params.x}} placeholder (in prompt, command, system_prompt, or a
//     whole routing field) references a declared param.
//  8. The task graph has no cycles.
//  9. Every prompt, command, and system_prompt (workflow- or task-level) is
//     free of malformed {{params.}} tokens; a system_prompt is free of
//     task-id placeholders.
//  10. Every declared param is referenced by at least one prompt, command,
//     routing field, or system_prompt (workflow- or task-level).
//  11. The effective runtime/model/effort and system prompt per LLM task are
//     accepted by the registered runtime spec (checked by ValidateRouting).
//     Shell and sub-workflow tasks bypass this check.
package workflow

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/mcuste/loom/pkg/runtime"
)

// Parse decodes a workflow YAML document and returns the validated Workflow.
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

	topTasks, rawLoops, err := splitLoopWrappers(raw.Tasks)
	if err != nil {
		return nil, err
	}

	// Only a workflow with nothing to run at all is rejected. A loop is an
	// independently scheduled unit (the executor spawns each loop's driver
	// directly, with no dependency on a top-level task to seed it), so a
	// loops-only workflow is valid; it is rejected only when no loops accompany
	// the empty top-level set.
	if len(topTasks) == 0 && len(rawLoops) == 0 {
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
		WorkingDir:   raw.WorkingDir,
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
	if err := validateRoutingField("", "runtime", raw.Runtime, paramSet); err != nil {
		return nil, err
	}
	if err := validateRoutingField("", "model", raw.Model, paramSet); err != nil {
		return nil, err
	}
	if err := validateRoutingField("", "effort", raw.Effort, paramSet); err != nil {
		return nil, err
	}

	allTasks, ids, err := flattenLoopTasks(topTasks, rawLoops)
	if err != nil {
		return nil, err
	}

	if err := validateLoopNamespace(rawLoops, ids, paramIdx); err != nil {
		return nil, err
	}

	loops, memberByLoop, err := buildLoopGroups(rawLoops, ids, paramSet)
	if err != nil {
		return nil, err
	}
	wf.Loops = loops

	// asByLoop maps each loop id to its for_each loop variable ("" for a while
	// loop), so the per-task build below can exempt a member's {{as}}
	// placeholder from the depends_on check (it is bound per iteration, not via
	// the DAG).
	asByLoop := make(map[LoopID]string, len(loops))
	for i := range loops {
		asByLoop[loops[i].ID] = loops[i].As
	}

	st := &parseState{
		wf:           wf,
		ids:          ids,
		paramSet:     paramSet,
		asByLoop:     asByLoop,
		memberByLoop: memberByLoop,
	}
	for _, lt := range allTasks {
		if err := buildTask(st, lt); err != nil {
			return nil, err
		}
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

	if raw.Schedule != nil {
		if raw.Schedule.Cron == "" {
			return nil, fmt.Errorf("schedule: cron is required")
		}
		wf.Schedule = &Schedule{Cron: raw.Schedule.Cron, TZ: raw.Schedule.TZ}
	}

	return wf, nil
}

func splitLoopWrappers(rawTasks []rawTask) ([]rawTask, []rawLoop, error) {
	// Loops are declared as tasks carrying a loop: (while) or for_each: block:
	// the wrapper is not an executable task; its id becomes the loop id and its
	// nested tasks: the members. Split wrappers out of the top-level task set
	// and collect them as rawLoops for the shared loop-group machinery.
	var rawLoops []rawLoop
	topTasks := make([]rawTask, 0, len(rawTasks))
	for _, rt := range rawTasks {
		isLoop := rt.Loop.Kind != 0
		isForEach := rt.ForEach.Kind != 0
		isForEachParallel := rt.ForEachParallel.Kind != 0
		if !isLoop && !isForEach && !isForEachParallel {
			topTasks = append(topTasks, rt)
			continue
		}
		tid, err := NewTaskID(rt.ID)
		if err != nil {
			return nil, nil, err
		}
		// loop:, for_each:, and for_each_parallel: are sibling scoped-block
		// wrappers; a task declaring more than one is ambiguous.
		var wrappers []string
		if isLoop {
			wrappers = append(wrappers, "loop")
		}
		if isForEach {
			wrappers = append(wrappers, "for_each")
		}
		if isForEachParallel {
			wrappers = append(wrappers, "for_each_parallel")
		}
		if len(wrappers) > 1 {
			return nil, nil, &TaskBodyConflictError{Task: tid, Fields: wrappers}
		}
		wrapper := wrappers[0]
		if err := rejectLoopWrapperFields(tid, rt, wrapper); err != nil {
			return nil, nil, err
		}
		rl := rawLoop{id: LoopID(tid), description: rt.Description}
		switch {
		case isForEach:
			rl.kind = LoopForEach
			if err := decodeForEachBody(&rt.ForEach, &rl); err != nil {
				return nil, nil, fmt.Errorf("task %q: %w", tid, err)
			}
		case isForEachParallel:
			rl.kind = LoopForEach
			rl.parallel = true
			if err := decodeForEachBody(&rt.ForEachParallel, &rl); err != nil {
				return nil, nil, fmt.Errorf("task %q: %w", tid, err)
			}
		default:
			if err := decodeLoopBody(&rt.Loop, &rl); err != nil {
				return nil, nil, fmt.Errorf("task %q: %w", tid, err)
			}
		}
		rawLoops = append(rawLoops, rl)
	}
	return topTasks, rawLoops, nil
}

func flattenLoopTasks(topTasks []rawTask, rawLoops []rawLoop) ([]loopTask, map[TaskID]struct{}, error) {
	// allTasks is the flat union of top-level and every loop's nested tasks, in
	// declaration order, each tagged with its owning loop ("" for top-level). The
	// whole parser runs over this list so wf.Tasks ends up flat and ordered, and
	// existing code over wf.Tasks (Plan, ByID, Effective, the scheduler) is
	// unchanged by the addition of scoped loops.
	allTasks := make([]loopTask, 0, len(topTasks)+len(rawLoops))
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
			return nil, nil, err
		}
		if _, dup := ids[tid]; dup {
			return nil, nil, &DuplicateTaskIDError{ID: tid}
		}
		ids[tid] = struct{}{}
	}

	return allTasks, ids, nil
}

func validateLoopNamespace(rawLoops []rawLoop, ids map[TaskID]struct{}, paramIdx map[ParamName]int) error {
	// Loop ids share the global namespace: each must be distinct from every task
	// id and param name, and unique across loops.
	seenLoops := make(map[LoopID]struct{}, len(rawLoops))
	for _, rl := range rawLoops {
		if _, ok := ids[TaskID(rl.id)]; ok {
			return &LoopIDCollisionError{Loop: rl.id, Kind: "task"}
		}
		if _, ok := paramIdx[ParamName(rl.id)]; ok {
			return &LoopIDCollisionError{Loop: rl.id, Kind: "param"}
		}
		if _, dup := seenLoops[rl.id]; dup {
			return &DuplicateLoopIDError{Loop: rl.id}
		}
		seenLoops[rl.id] = struct{}{}
	}
	return nil
}

// ValidateRouting checks every LLM task's effective runtime/model/effort against
// the runtime registry, recursing into linked sub-workflows. It is the
// registry-dependent companion to [Parse]: keeping it separate makes Parse a
// pure function of its input bytes (identical bytes always parse identically,
// independent of which runtime init() functions have run), and lets callers run
// the routing check explicitly once the registry is populated and any
// sub-workflow children are linked into w.Subs.
//
// Shell, script, and sub-workflow tasks bypass the registry entirely
// (runtime/model/effort have no meaning for them; a sub-workflow's child brings
// its own), so they are skipped here and reached only through the w.Subs
// recursion.
func (w *Workflow) ValidateRouting() error {
	params, _ := ResolveParams(w, nil, nil)
	return w.ValidateRoutingWithParams(params, true)
}

// ValidateRoutingWithParams checks routing after substituting whole-field
// `{{params.name}}` values in runtime/model/effort. When allowUnresolved is
// true, any task whose routing still depends on a missing param is skipped so
// advisory checks can still render a plan for workflows with required params.
func (w *Workflow) ValidateRoutingWithParams(params ParamValues, allowUnresolved bool) error {
	for i := range w.Tasks {
		t := &w.Tasks[i]
		if t.IsShell() || t.IsSubWorkflow() || t.IsScript() {
			continue
		}
		if allowUnresolved && w.routingNeedsMissingParam(t, params) {
			continue
		}
		rt, m, e := w.EffectiveWithParams(t, params)
		req := runtime.Request{
			TaskID:       string(t.ID),
			Prompt:       t.Prompt,
			Model:        m,
			Effort:       e,
			SystemPrompt: w.EffectiveSystemPrompt(t),
		}
		if err := runtime.Validate(rt, req); err != nil {
			return fmt.Errorf("task %q: %w", t.ID, err)
		}
	}
	for _, child := range w.Subs {
		childParams, _ := ResolveParams(child, nil, nil)
		if err := child.ValidateRoutingWithParams(childParams, true); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workflow) routingNeedsMissingParam(t *Task, params ParamValues) bool {
	for _, value := range []string{
		string(t.Runtime), string(w.Runtime),
		string(t.Model), string(w.Model),
		string(t.Effort), string(w.Effort),
	} {
		name, ok := wholeParamPlaceholder(value)
		if !ok {
			continue
		}
		if _, found := params[name]; !found {
			return true
		}
	}
	return false
}
