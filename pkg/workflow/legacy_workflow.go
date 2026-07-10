package workflow

// This file contains compatibility adapters between the semantic Definition
// model and the legacy Workflow/Task materialized view. New workflow pipeline
// code should prefer Definition, TaskNode, and Action directly.

// Task returns the legacy materialized Task view for this semantic task node.
func (n TaskNode) Task() Task { return taskFromNode(n) }

func workflowFromDefinition(def WorkflowDefinition) *Workflow {
	def = cloneWorkflowDefinition(def)
	wf := &Workflow{
		ID:                   def.ID,
		Description:          def.Description,
		Runtime:              def.Defaults.Runtime,
		Model:                def.Defaults.Model,
		Effort:               def.Defaults.Effort,
		SystemPrompt:         def.Defaults.SystemPrompt.String(),
		systemPromptTemplate: def.Defaults.SystemPrompt,
		Cache:                def.Defaults.Cache,
		WorkingDir:           def.Defaults.WorkingDir,
		Params:               append([]Param(nil), def.Params...),
		Budget:               def.Policies.Budget,
		Output:               def.Output.Task,
		Schedule:             cloneSchedule(def.Schedule),
		byID:                 make(map[TaskID]int),
		paramByName:          make(map[ParamName]int, len(def.Params)),
	}
	if !wf.systemPromptTemplate.parsed {
		wf.systemPromptTemplate = ParseTemplate(wf.SystemPrompt)
	}
	for i := range wf.Params {
		wf.paramByName[wf.Params[i].Name] = i
	}
	for _, node := range def.Nodes {
		switch n := node.(type) {
		case TaskNode:
			wf.appendTask(taskFromNode(n))
		case LoopNode:
			wf.Loops = append(wf.Loops, cloneLoopGroup(n.Spec))
			for _, task := range n.Body.Nodes {
				wf.appendTask(taskFromNode(task))
			}
		}
	}
	wf.storeDefinition(def)
	return wf
}

func (w *Workflow) appendTask(t Task) {
	w.byID[t.ID] = len(w.Tasks)
	w.Tasks = append(w.Tasks, t)
}

func taskFromNode(n TaskNode) Task {
	action := cloneAction(n.Action)
	t := Task{
		ID:                   n.ID,
		Description:          n.Description,
		DependsOn:            taskIDs(n.DependsOn),
		When:                 n.When,
		Cond:                 n.Condition,
		Runtime:              n.Runtime.Runtime,
		Model:                n.Runtime.Model,
		Effort:               n.Runtime.Effort,
		Retry:                n.Policies.Retry,
		WritesState:          n.WritesState,
		Budget:               n.Policies.Budget,
		Schema:               n.Policies.Schema,
		Cache:                n.Policies.Cache,
		Loop:                 n.Loop,
		OkExit:               append([]int(nil), n.Policies.OkExit...),
		SystemPrompt:         n.SystemPrompt.String(),
		systemPromptTemplate: n.SystemPrompt,
		action:               action,
	}
	if !t.systemPromptTemplate.parsed {
		t.systemPromptTemplate = ParseTemplate(t.SystemPrompt)
	}
	switch a := action.(type) {
	case PromptAction:
		t.Prompt = a.Prompt.String()
	case CommandAction:
		t.Command = a.Command.String()
	case ScriptAction:
		t.Script = a.Path.String()
		t.Args = templateStrings(a.Args)
	case SubWorkflowAction:
		t.Workflow = string(a.Ref)
		t.With = append([]WithArg(nil), a.With...)
	}
	return t
}

func templateStrings(values []Template) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = value.String()
	}
	return out
}

func taskIDs(ids []NodeID) []TaskID {
	out := make([]TaskID, len(ids))
	for i, id := range ids {
		out[i] = TaskID(id)
	}
	return out
}

func cloneSchedule(s *Schedule) *Schedule {
	if s == nil {
		return nil
	}
	out := *s
	return &out
}

func buildDefinitionFromWorkflow(w *Workflow) WorkflowDefinition {
	defaultSystemPrompt := w.systemPromptTemplate
	if !defaultSystemPrompt.parsed {
		defaultSystemPrompt = ParseTemplate(w.SystemPrompt)
	}
	def := WorkflowDefinition{
		ID:          w.ID,
		Description: w.Description,
		Defaults: WorkflowDefaults{
			Runtime:      w.Runtime,
			Model:        w.Model,
			Effort:       w.Effort,
			SystemPrompt: defaultSystemPrompt,
			WorkingDir:   w.WorkingDir,
			Cache:        w.Cache,
		},
		Params: append([]Param(nil), w.Params...),
		Order:  append([]TaskID(nil), w.Plan()...),
		Output: OutputSelector{Task: w.Output},
		Policies: WorkflowPolicies{
			Budget: w.Budget,
			Cache:  w.Cache,
		},
		Schedule: cloneSchedule(w.Schedule),
	}
	memberLoop := make(map[TaskID]LoopID)
	for i := range w.Loops {
		lg := &w.Loops[i]
		for _, member := range lg.Members {
			memberLoop[member] = lg.ID
		}
	}
	for i := range w.Tasks {
		t := &w.Tasks[i]
		if _, loopMember := memberLoop[t.ID]; loopMember {
			continue
		}
		def.Nodes = append(def.Nodes, nodeFromTask(t))
	}
	for i := range w.Loops {
		lg := w.Loops[i]
		body := WorkflowFragment{Nodes: make([]TaskNode, 0, len(lg.Members))}
		for _, member := range lg.Members {
			if t := w.ByID(member); t != nil && memberLoop[t.ID] == lg.ID {
				body.Nodes = append(body.Nodes, nodeFromTask(t))
			}
		}
		def.Nodes = append(def.Nodes, LoopNode{ID: lg.ID, Description: lg.Description, Spec: lg, Body: body})
	}
	return def
}

func nodeFromTask(t *Task) TaskNode {
	systemPrompt := t.systemPromptTemplate
	if !systemPrompt.parsed {
		systemPrompt = ParseTemplate(t.SystemPrompt)
	}
	return TaskNode{
		ID:          t.ID,
		Description: t.Description,
		DependsOn:   nodeIDs(t.DependsOn),
		Action:      t.Action(),
		Condition:   t.Cond,
		When:        t.When,
		Runtime: RuntimeSelector{
			Runtime: t.Runtime,
			Model:   t.Model,
			Effort:  t.Effort,
		},
		Policies: TaskPolicies{
			Retry:  t.Retry,
			Budget: t.Budget,
			Cache:  t.Cache,
			Schema: t.Schema,
			OkExit: append([]int(nil), t.OkExit...),
		},
		WritesState:  t.WritesState,
		Loop:         t.Loop,
		SystemPrompt: systemPrompt,
	}
}

func nodeIDs(ids []TaskID) []NodeID {
	out := make([]NodeID, len(ids))
	for i, id := range ids {
		out[i] = NodeID(id)
	}
	return out
}
