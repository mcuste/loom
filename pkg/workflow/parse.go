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
//
// Runtime-catalog validation is intentionally outside this parser; callers use
// pkg/workflowcheck after params are resolved and sub-workflows are linked.
package workflow

import (
	"fmt"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/syntax"
)

// ParseOptions configures conversion from syntax draft to Workflow.
type ParseOptions struct {
	Source syntax.Source
}

type parser struct {
	doc          *syntax.Document
	id           WorkflowID
	wf           *Workflow
	paramSet     map[ParamName]struct{}
	paramIdx     map[ParamName]int
	memberByLoop map[LoopID]map[TaskID]bool
}

// Parse decodes a workflow YAML document and returns the validated Workflow.
func Parse(data []byte) (*Workflow, error) {
	doc, err := syntax.Decode(data, syntax.Source{})
	if err != nil {
		return nil, err
	}
	return FromDocument(doc, ParseOptions{})
}

// FromDraft constructs a validated Workflow from a decoded syntax draft.
func FromDraft(draft *syntax.Draft, opts ParseOptions) (*Workflow, error) {
	return FromDocument((*syntax.Document)(draft), opts)
}

// FromDocument constructs a validated Workflow from a decoded syntax document.
func FromDocument(doc *syntax.Document, opts ParseOptions) (*Workflow, error) {
	p, err := newParser(doc, opts)
	if err != nil {
		return nil, err
	}
	return p.parse()
}

func newParser(doc *syntax.Document, opts ParseOptions) (*parser, error) {
	if doc == nil {
		return nil, fmt.Errorf("workflow document is nil")
	}
	if opts.Source.Path != "" {
		doc.Source = opts.Source
	}
	id, err := NewWorkflowID(doc.Name)
	if err != nil {
		return nil, err
	}
	return &parser{doc: doc, id: id}, nil
}

func (p *parser) parse() (*Workflow, error) {
	topTasks, rawLoops, err := p.collectDeclarations()
	if err != nil {
		return nil, err
	}

	allTasks, st, err := p.resolveLoopScopes(topTasks, rawLoops)
	if err != nil {
		return nil, err
	}

	if err := lowerAllTasks(st, allTasks); err != nil {
		return nil, err
	}

	return p.validateAndFinalize()
}

func (p *parser) collectDeclarations() ([]syntax.DraftTask, []rawLoop, error) {
	topTasks, rawLoops, err := prepareDraftLoops(p.id, p.doc.Tasks)
	if err != nil {
		return nil, nil, err
	}
	if err := p.buildSkeleton(); err != nil {
		return nil, nil, err
	}
	return topTasks, rawLoops, nil
}

func prepareDraftLoops(id WorkflowID, draftTasks []syntax.DraftTask) ([]syntax.DraftTask, []rawLoop, error) {
	topTasks, rawLoops, err := splitLoopWrappers(draftTasks)
	if err != nil {
		return nil, nil, err
	}

	// Only a workflow with nothing to run at all is rejected. A loop is an
	// independently scheduled unit (the executor spawns each loop's driver
	// directly, with no dependency on a top-level task to seed it), so a
	// loops-only workflow is valid; it is rejected only when no loops accompany
	// the empty top-level set.
	if len(topTasks) == 0 && len(rawLoops) == 0 {
		return nil, nil, fmt.Errorf("workflow %q: %w", id, ErrNoTasks)
	}

	return topTasks, rawLoops, nil
}

func (p *parser) buildSkeleton() error {
	params, paramIdx, err := parseParams(p.doc.Params)
	if err != nil {
		return err
	}

	cache := false
	if p.doc.Cache != nil {
		cache = *p.doc.Cache
	}
	p.wf = &Workflow{
		ID:                   p.id,
		Description:          p.doc.Description,
		Runtime:              runtime.Name(p.doc.Runtime),
		Model:                runtime.Model(p.doc.Model),
		Effort:               runtime.Effort(p.doc.Effort),
		SystemPrompt:         p.doc.SystemPrompt,
		systemPromptTemplate: ParseTemplate(p.doc.SystemPrompt),
		Cache:                cache,
		WorkingDir:           p.doc.WorkingDir,
		Params:               params,
		Tasks:                make([]Task, 0, len(p.doc.Tasks)),
		byID:                 make(map[TaskID]int, len(p.doc.Tasks)),
		paramByName:          paramIdx,
	}

	p.paramIdx = paramIdx
	p.paramSet = paramSetFromIndex(paramIdx)
	if err := validateRoutingField("", "runtime", p.doc.Runtime, p.paramSet); err != nil {
		return err
	}
	if err := validateRoutingField("", "model", p.doc.Model, p.paramSet); err != nil {
		return err
	}
	if err := validateRoutingField("", "effort", p.doc.Effort, p.paramSet); err != nil {
		return err
	}

	return nil
}

func paramSetFromIndex(paramIdx map[ParamName]int) map[ParamName]struct{} {
	paramSet := make(map[ParamName]struct{}, len(paramIdx))
	for name := range paramIdx {
		paramSet[name] = struct{}{}
	}
	return paramSet
}

func (p *parser) resolveLoopScopes(topTasks []syntax.DraftTask, rawLoops []rawLoop) ([]taskDecl, *parseState, error) {
	allTasks, ids, err := flattenLoopTasks(topTasks, rawLoops)
	if err != nil {
		return nil, nil, err
	}

	if err := validateLoopNamespace(rawLoops, ids, p.paramIdx); err != nil {
		return nil, nil, err
	}

	loops, memberByLoop, err := buildLoopGroups(rawLoops, ids, p.paramSet)
	if err != nil {
		return nil, nil, err
	}
	p.wf.Loops = loops
	p.memberByLoop = memberByLoop

	// asByLoop maps each loop id to its for_each loop variable ("" for a while
	// loop), so the per-task build below can exempt a member's {{as}}
	// placeholder from the depends_on check (it is bound per iteration, not via
	// the DAG).
	asByLoop := make(map[LoopID]string, len(loops))
	for i := range loops {
		asByLoop[loops[i].ID] = loops[i].As
	}

	st := &parseState{
		wf:           p.wf,
		ids:          ids,
		paramSet:     p.paramSet,
		asByLoop:     asByLoop,
		memberByLoop: memberByLoop,
	}

	return allTasks, st, nil
}

func lowerAllTasks(st *parseState, allTasks []taskDecl) error {
	for _, lt := range allTasks {
		if err := buildTask(st, lt); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) validateAndFinalize() (*Workflow, error) {
	if p.doc.Output != "" {
		outputTask := TaskID(p.doc.Output)
		if _, ok := p.wf.byID[outputTask]; !ok {
			return nil, &UnknownOutputTaskError{Task: outputTask}
		}
		p.wf.Output = outputTask
	}

	if err := checkPrevPlaceholders(p.wf, p.memberByLoop); err != nil {
		return nil, err
	}

	if err := validateSystemPrompt(p.wf.SystemPrompt, p.paramSet); err != nil {
		return nil, err
	}

	if cycle, ok := findCycle(p.wf); ok {
		return nil, &CycleError{Cycle: cycle}
	}

	if err := checkUnusedParams(p.wf); err != nil {
		return nil, err
	}

	budget, err := parseBudget(p.doc.Budget)
	if err != nil {
		return nil, err
	}
	p.wf.Budget = budget

	if p.doc.Schedule != nil {
		if p.doc.Schedule.Cron == "" {
			return nil, fmt.Errorf("schedule: cron is required")
		}
		p.wf.Schedule = &Schedule{Cron: p.doc.Schedule.Cron, TZ: p.doc.Schedule.TZ}
	}

	return p.wf, nil
}

func splitLoopWrappers(draftTasks []syntax.DraftTask) ([]syntax.DraftTask, []rawLoop, error) {
	// Loops are declared as tasks carrying a loop: (while) or for_each: block:
	// the wrapper is not an executable task; its id becomes the loop id and its
	// nested tasks: the members. Split wrappers out of the top-level task set
	// and collect them as rawLoops for the shared loop-group machinery.
	var rawLoops []rawLoop
	topTasks := make([]syntax.DraftTask, 0, len(draftTasks))
	for _, rt := range draftTasks {
		isLoop := rt.Loop.Present()
		isForEach := rt.ForEach.Present()
		isForEachParallel := rt.ForEachParallel.Present()
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
			if err := decodeForEachBody(rt.ForEach, &rl); err != nil {
				return nil, nil, fmt.Errorf("task %q: %w", tid, err)
			}
		case isForEachParallel:
			rl.kind = LoopForEach
			rl.parallel = true
			if err := decodeForEachBody(rt.ForEachParallel, &rl); err != nil {
				return nil, nil, fmt.Errorf("task %q: %w", tid, err)
			}
		default:
			if err := decodeLoopBody(rt.Loop, &rl); err != nil {
				return nil, nil, fmt.Errorf("task %q: %w", tid, err)
			}
		}
		rawLoops = append(rawLoops, rl)
	}
	return topTasks, rawLoops, nil
}

func flattenLoopTasks(topTasks []syntax.DraftTask, rawLoops []rawLoop) ([]taskDecl, map[TaskID]struct{}, error) {
	// allTasks is the flat union of top-level and every loop's nested tasks, in
	// declaration order, each tagged with its owning loop ("" for top-level). The
	// whole parser runs over this list so wf.Tasks ends up flat and ordered, and
	// existing code over wf.Tasks (Plan, ByID, Effective, the scheduler) is
	// unchanged by the addition of scoped loops.
	allTasks := make([]taskDecl, 0, len(topTasks)+len(rawLoops))
	for _, rt := range topTasks {
		decl, err := newTaskDecl(rt, "")
		if err != nil {
			return nil, nil, err
		}
		allTasks = append(allTasks, decl)
	}
	for _, rl := range rawLoops {
		for _, rt := range rl.tasks {
			decl, err := newTaskDecl(rt, rl.id)
			if err != nil {
				return nil, nil, err
			}
			allTasks = append(allTasks, decl)
		}
	}

	// Global task-id uniqueness across top-level and every loop's nested tasks: a
	// task lives in a single flat namespace regardless of which loop defines it.
	ids := make(map[TaskID]struct{}, len(allTasks))
	for _, task := range allTasks {
		if _, dup := ids[task.id]; dup {
			return nil, nil, &DuplicateTaskIDError{ID: task.id}
		}
		ids[task.id] = struct{}{}
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
