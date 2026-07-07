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
// The load-time pipeline is intentionally layered:
//
//  1. pkg/syntax decodes YAML into syntax.Document without assigning domain
//     meaning.
//  2. collectDeclarations validates identifiers and lowers raw syntax into
//     parser declarations, preserving raw values for semantic fields such as
//     retry:, budget:, schema:, loop:, and with:.
//  3. validateDeclarations validates cross-declaration invariants such as task
//     uniqueness, loop namespaces, loop membership, and loop convergence.
//  4. lowerWorkflow builds the validated Workflow/Definition model, parsing
//     templates, conditions, retry, budget, schema, and dependency references.
//  5. Invocation-time checks that need resolved params, linked sub-workflows, or
//     a runtime catalog stay outside Parse in pkg/workflowcheck/pkg/workflowload.
//
// Validation performed by Parse includes:
//
//  1. Workflow name and every task id satisfy [A-Za-z0-9_]+.
//  2. Task ids are unique.
//  3. Param block: names are valid, unique, required-vs-default is exclusive,
//     defaults are scalar strings.
//  4. Every task sets exactly one body form. A shell/script task must not set
//     task-level runtime, model, effort, system_prompt, or schema.
//  5. Every depends_on entry names a known task and appears at most once.
//  6. Every {{id}} placeholder in a prompt, command, script, or with-value is a
//     member of that task's depends_on. Placeholders are pure templating; they
//     never extend the dependency graph implicitly.
//  7. Every {{params.x}} placeholder references a declared param.
//  8. Loop wrappers, loop namespaces, and loop convergence targets are valid.
//  9. The task graph has no cycles.
//  10. Every prompt, command, script, with-value, and system_prompt is free of
//     malformed {{params.}} tokens; a system_prompt is free of task-id
//     placeholders.
//  11. Every declared param is referenced by at least one prompt, command,
//     script, with-value, routing field, or system_prompt.
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
	doc *syntax.Document
	id  WorkflowID
}

// workflowDecl is the parser's declaration model. It is no longer raw YAML, but
// it is not executable yet: syntax.Value fields are retained until the semantic
// lowering phase can validate them with the full task/param/loop scope.
type workflowDecl struct {
	id           WorkflowID
	description  string
	runtime      runtime.Name
	model        runtime.Model
	effort       runtime.Effort
	systemPrompt string
	params       []Param
	paramIdx     map[ParamName]int
	paramSet     map[ParamName]struct{}
	topTasks     []syntax.DraftTask
	rawLoops     []rawLoop
	budget       syntax.Value
	cache        bool
	workingDir   string
	output       string
	schedule     *syntax.DraftSchedule
}

// checkedWorkflowDecl is the validated declaration graph ready to lower into
// the workflow semantic model. It keeps graph/loop lookup tables beside the
// declarations so the lowering step does not reach back into syntax.Document.
type checkedWorkflowDecl struct {
	decl         workflowDecl
	tasks        []taskDecl
	ids          map[TaskID]struct{}
	loops        []LoopGroup
	memberByLoop map[LoopID]map[TaskID]bool
	asByLoop     map[LoopID]string
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
	decl, err := p.collectDeclarations()
	if err != nil {
		return nil, err
	}

	checked, err := validateDeclarations(decl)
	if err != nil {
		return nil, err
	}

	return lowerWorkflow(checked)
}

func (p *parser) collectDeclarations() (workflowDecl, error) {
	topTasks, rawLoops, err := prepareDraftLoops(p.id, p.doc.Tasks)
	if err != nil {
		return workflowDecl{}, err
	}

	params, paramIdx, err := parseParams(p.doc.Params)
	if err != nil {
		return workflowDecl{}, err
	}

	cache := false
	if p.doc.Cache != nil {
		cache = *p.doc.Cache
	}
	decl := workflowDecl{
		id:           p.id,
		description:  p.doc.Description,
		runtime:      runtime.Name(p.doc.Runtime),
		model:        runtime.Model(p.doc.Model),
		effort:       runtime.Effort(p.doc.Effort),
		systemPrompt: p.doc.SystemPrompt,
		params:       params,
		paramIdx:     paramIdx,
		paramSet:     paramSetFromIndex(paramIdx),
		topTasks:     topTasks,
		rawLoops:     rawLoops,
		budget:       p.doc.Budget,
		cache:        cache,
		workingDir:   p.doc.WorkingDir,
		output:       p.doc.Output,
		schedule:     p.doc.Schedule,
	}

	if err := validateRoutingField("", "runtime", p.doc.Runtime, decl.paramSet); err != nil {
		return workflowDecl{}, err
	}
	if err := validateRoutingField("", "model", p.doc.Model, decl.paramSet); err != nil {
		return workflowDecl{}, err
	}
	if err := validateRoutingField("", "effort", p.doc.Effort, decl.paramSet); err != nil {
		return workflowDecl{}, err
	}

	return decl, nil
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

func paramSetFromIndex(paramIdx map[ParamName]int) map[ParamName]struct{} {
	paramSet := make(map[ParamName]struct{}, len(paramIdx))
	for name := range paramIdx {
		paramSet[name] = struct{}{}
	}
	return paramSet
}

func validateDeclarations(decl workflowDecl) (checkedWorkflowDecl, error) {
	allTasks, ids, err := flattenLoopTasks(decl.topTasks, decl.rawLoops)
	if err != nil {
		return checkedWorkflowDecl{}, err
	}

	if err := validateLoopNamespace(decl.rawLoops, ids, decl.paramIdx); err != nil {
		return checkedWorkflowDecl{}, err
	}

	loops, memberByLoop, err := buildLoopGroups(decl.rawLoops, ids, decl.paramSet)
	if err != nil {
		return checkedWorkflowDecl{}, err
	}

	// asByLoop maps each loop id to its for_each loop variable ("" for a while
	// loop), so the per-task build below can exempt a member's {{as}}
	// placeholder from the depends_on check (it is bound per iteration, not via
	// the DAG).
	asByLoop := make(map[LoopID]string, len(loops))
	for i := range loops {
		asByLoop[loops[i].ID] = loops[i].As
	}

	return checkedWorkflowDecl{
		decl:         decl,
		tasks:        allTasks,
		ids:          ids,
		loops:        loops,
		memberByLoop: memberByLoop,
		asByLoop:     asByLoop,
	}, nil
}

func lowerWorkflow(checked checkedWorkflowDecl) (*Workflow, error) {
	wf := checked.decl.newWorkflow(len(checked.tasks))
	wf.Loops = checked.loops

	st := &parseState{
		wf:       wf,
		ids:      checked.ids,
		paramSet: checked.decl.paramSet,
		asByLoop: checked.asByLoop,
	}
	if err := lowerAllTasks(st, checked.tasks); err != nil {
		return nil, err
	}

	return finalizeWorkflow(wf, checked)
}

func (d workflowDecl) newWorkflow(taskCap int) *Workflow {
	paramIdx := make(map[ParamName]int, len(d.paramIdx))
	for name, idx := range d.paramIdx {
		paramIdx[name] = idx
	}
	return &Workflow{
		ID:                   d.id,
		Description:          d.description,
		Runtime:              d.runtime,
		Model:                d.model,
		Effort:               d.effort,
		SystemPrompt:         d.systemPrompt,
		systemPromptTemplate: ParseTemplate(d.systemPrompt),
		Cache:                d.cache,
		WorkingDir:           d.workingDir,
		Params:               append([]Param(nil), d.params...),
		Tasks:                make([]Task, 0, taskCap),
		byID:                 make(map[TaskID]int, taskCap),
		paramByName:          paramIdx,
	}
}

func lowerAllTasks(st *parseState, allTasks []taskDecl) error {
	for _, lt := range allTasks {
		if err := buildTask(st, lt); err != nil {
			return err
		}
	}
	return nil
}

func finalizeWorkflow(wf *Workflow, checked checkedWorkflowDecl) (*Workflow, error) {
	decl := checked.decl
	if decl.output != "" {
		outputTask := TaskID(decl.output)
		if _, ok := wf.byID[outputTask]; !ok {
			return nil, &UnknownOutputTaskError{Task: outputTask}
		}
		wf.Output = outputTask
	}

	if err := checkPrevPlaceholders(wf, checked.memberByLoop); err != nil {
		return nil, err
	}

	if err := validateSystemPrompt(wf.SystemPrompt, decl.paramSet); err != nil {
		return nil, err
	}

	if cycle, ok := findCycle(wf); ok {
		return nil, &CycleError{Cycle: cycle}
	}

	if err := checkUnusedParams(wf); err != nil {
		return nil, err
	}

	budget, err := parseBudget(decl.budget)
	if err != nil {
		return nil, err
	}
	wf.Budget = budget

	if decl.schedule != nil {
		if decl.schedule.Cron == "" {
			return nil, fmt.Errorf("schedule: cron is required")
		}
		wf.Schedule = &Schedule{Cron: decl.schedule.Cron, TZ: decl.schedule.TZ}
	}

	// Freeze the semantic node model after the legacy view is fully materialized.
	wf.storeDefinition()
	return wf, nil
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
