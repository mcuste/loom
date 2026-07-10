package workflow

func lowerCheckedDefinition(checked checkedWorkflowDecl) (Definition, error) {
	def := checked.decl.newDefinition()
	st := &parseState{
		ids:      checked.ids,
		paramSet: checked.decl.paramSet,
		asByLoop: checked.asByLoop,
	}
	tasks, err := lowerTaskNodes(st, checked.tasks)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	def.Nodes = workflowNodesFromTasks(tasks, checked.loops)
	return finalizeDefinition(def, checked)
}

func (d workflowDecl) newDefinition() Definition {
	return WorkflowDefinition{
		ID:          d.id,
		Description: d.description,
		Defaults: WorkflowDefaults{
			Runtime:      d.runtime,
			Model:        d.model,
			Effort:       d.effort,
			SystemPrompt: ParseTemplate(d.systemPrompt),
			WorkingDir:   d.workingDir,
			Cache:        d.cache,
		},
		Params: append([]Param(nil), d.params...),
		Policies: WorkflowPolicies{
			Cache: d.cache,
		},
	}
}

func lowerTaskNodes(st *parseState, allTasks []taskDecl) ([]TaskNode, error) {
	tasks := make([]TaskNode, 0, len(allTasks))
	for _, lt := range allTasks {
		task, err := buildTaskNode(st, lt)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func workflowNodesFromTasks(tasks []TaskNode, loops []LoopGroup) []WorkflowNode {
	taskByID := make(map[TaskID]TaskNode, len(tasks))
	loopMember := make(map[TaskID]bool)
	for _, task := range tasks {
		taskByID[task.ID] = task
		if task.Loop != "" {
			loopMember[task.ID] = true
		}
	}

	nodes := make([]WorkflowNode, 0, len(tasks)+len(loops))
	for _, task := range tasks {
		if loopMember[task.ID] {
			continue
		}
		nodes = append(nodes, task)
	}
	for _, loop := range loops {
		body := WorkflowFragment{Nodes: make([]TaskNode, 0, len(loop.Members))}
		for _, member := range loop.Members {
			body.Nodes = append(body.Nodes, taskByID[member])
		}
		nodes = append(nodes, LoopNode{ID: loop.ID, Description: loop.Description, Spec: cloneLoopGroup(loop), Body: body})
	}
	return nodes
}

func finalizeDefinition(def Definition, checked checkedWorkflowDecl) (Definition, error) {
	decl := checked.decl
	if decl.output != "" {
		outputTask := TaskID(decl.output)
		if !definitionHasTask(def, outputTask) {
			return WorkflowDefinition{}, &UnknownOutputTaskError{Task: outputTask}
		}
		def.Output = OutputSelector{Task: outputTask}
	}

	if err := checkPrevPlaceholdersDefinition(def, checked.memberByLoop); err != nil {
		return WorkflowDefinition{}, err
	}

	if err := validateSystemPrompt(def.Defaults.SystemPrompt.String(), decl.paramSet); err != nil {
		return WorkflowDefinition{}, err
	}

	if cycle, ok := findCycleDefinition(def); ok {
		return WorkflowDefinition{}, &CycleError{Cycle: cycle}
	}

	if err := checkUnusedParamsDefinition(def); err != nil {
		return WorkflowDefinition{}, err
	}

	def.Policies.Budget = decl.budget
	def.Schedule = cloneSchedule(decl.schedule)

	def.Order = planDefinition(def)
	return def, nil
}
