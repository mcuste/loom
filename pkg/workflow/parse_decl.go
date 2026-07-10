package workflow

import (
	"fmt"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/syntax"
)

// workflowDecl is the parser's declaration model. It is no longer raw YAML and
// carries only workflow-owned declaration values, but it is not executable yet:
// cross-reference validation and lowering still need the full task/param/loop
// scope.
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
	topTasks     []taskDecl
	rawLoops     []rawLoop
	budget       *Budget
	cache        bool
	workingDir   string
	output       string
	schedule     *Schedule
}

func (p *parser) parseDeclarations() (workflowDecl, error) {
	topTasks, rawLoops, err := prepareDraftLoops(p.id, p.doc.Tasks)
	if err != nil {
		return workflowDecl{}, err
	}

	params, paramIdx, err := parseParams(p.doc.Params)
	if err != nil {
		return workflowDecl{}, err
	}

	budget, err := parseBudget(p.doc.Budget)
	if err != nil {
		return workflowDecl{}, err
	}
	schedule, err := parseSchedule(p.doc.Schedule)
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
		budget:       budget,
		cache:        cache,
		workingDir:   p.doc.WorkingDir,
		output:       p.doc.Output,
		schedule:     schedule,
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

func prepareDraftLoops(id WorkflowID, draftTasks []syntax.DraftTask) ([]taskDecl, []rawLoop, error) {
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

func parseSchedule(raw *syntax.DraftSchedule) (*Schedule, error) {
	if raw == nil {
		return nil, nil
	}
	if raw.Cron == "" {
		return nil, fmt.Errorf("schedule: cron is required")
	}
	return &Schedule{Cron: raw.Cron, TZ: raw.TZ}, nil
}

func splitLoopWrappers(draftTasks []syntax.DraftTask) ([]taskDecl, []rawLoop, error) {
	// Loops are declared as tasks carrying a loop: (while) or for_each: block:
	// the wrapper is not an executable task; its id becomes the loop id and its
	// nested tasks: the members. Split wrappers out of the top-level task set
	// and collect them as rawLoops for the shared loop-group machinery.
	var rawLoops []rawLoop
	topTasks := make([]taskDecl, 0, len(draftTasks))
	for _, rt := range draftTasks {
		isLoop := rt.Loop.Present()
		isForEach := rt.ForEach.Present()
		isForEachParallel := rt.ForEachParallel.Present()
		if !isLoop && !isForEach && !isForEachParallel {
			decl, err := newTaskDecl(rt, "")
			if err != nil {
				return nil, nil, err
			}
			topTasks = append(topTasks, decl)
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
