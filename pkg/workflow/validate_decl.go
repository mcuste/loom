package workflow

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

func validateDeclarationGraph(decl workflowDecl) (checkedWorkflowDecl, error) {
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

func flattenLoopTasks(topTasks []taskDecl, rawLoops []rawLoop) ([]taskDecl, map[TaskID]struct{}, error) {
	// allTasks is the flat union of top-level and every loop's nested tasks, in
	// declaration order, each tagged with its owning loop ("" for top-level). The
	// whole parser runs over this list so wf.Tasks ends up flat and ordered, and
	// existing code over wf.Tasks (Plan, ByID, Effective, the scheduler) is
	// unchanged by the addition of scoped loops.
	allTasks := make([]taskDecl, 0, len(topTasks)+len(rawLoops))
	allTasks = append(allTasks, topTasks...)
	for _, rl := range rawLoops {
		allTasks = append(allTasks, rl.tasks...)
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
