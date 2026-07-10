package workflow

func cloneWorkflowDefinition(def WorkflowDefinition) WorkflowDefinition {
	out := def
	out.Params = append([]Param(nil), def.Params...)
	out.Order = append([]TaskID(nil), def.Order...)
	out.Schedule = cloneSchedule(def.Schedule)
	out.Nodes = make([]WorkflowNode, 0, len(def.Nodes))
	for _, node := range def.Nodes {
		out.Nodes = append(out.Nodes, cloneWorkflowNode(node))
	}
	return out
}

func cloneWorkflowNode(node WorkflowNode) WorkflowNode {
	switch n := node.(type) {
	case TaskNode:
		return cloneTaskNode(n)
	case LoopNode:
		return cloneLoopNode(n)
	default:
		return node
	}
}

func cloneTaskNode(n TaskNode) TaskNode {
	n.DependsOn = append([]NodeID(nil), n.DependsOn...)
	n.Policies.OkExit = append([]int(nil), n.Policies.OkExit...)
	n.Action = cloneAction(n.Action)
	return n
}

func cloneLoopNode(n LoopNode) LoopNode {
	bodyNodes := n.Body.Nodes
	n.Spec = cloneLoopGroup(n.Spec)
	n.Body.Nodes = make([]TaskNode, len(bodyNodes))
	for i, task := range bodyNodes {
		n.Body.Nodes[i] = cloneTaskNode(task)
	}
	return n
}

func cloneLoopGroup(g LoopGroup) LoopGroup {
	g.List = append([]string(nil), g.List...)
	g.Members = append([]TaskID(nil), g.Members...)
	return g
}

func cloneAction(action Action) Action {
	switch a := action.(type) {
	case ScriptAction:
		a.Args = append([]Template(nil), a.Args...)
		return a
	case SubWorkflowAction:
		a.With = append([]WithArg(nil), a.With...)
		a.WithTemplates = append([]WithTemplate(nil), a.WithTemplates...)
		return a
	default:
		return action
	}
}
