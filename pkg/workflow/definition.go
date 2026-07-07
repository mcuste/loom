package workflow

import "github.com/mcuste/loom/pkg/runtime"

// NodeID identifies any workflow graph node (task or structural loop).
type NodeID string

// WorkflowDefaults are inherited by executable task nodes.
type WorkflowDefaults struct {
	Runtime      runtime.Name
	Model        runtime.Model
	Effort       runtime.Effort
	SystemPrompt Template
	WorkingDir   string
	Cache        bool
}

// OutputSelector selects the task whose output is the workflow result.
type OutputSelector struct {
	Task TaskID
}

// WorkflowPolicies groups workflow-level execution policies.
type WorkflowPolicies struct {
	Budget *Budget
	Cache  bool
}

// RuntimeSelector is a task-level runtime override set.
type RuntimeSelector struct {
	Runtime runtime.Name
	Model   runtime.Model
	Effort  runtime.Effort
}

// TaskPolicies groups task-level policies.
type TaskPolicies struct {
	Retry  Retry
	Budget *Budget
	Cache  *bool
	Schema *Schema
	OkExit []int
}

// WorkflowNode is either an executable task node or a structural loop node.
type WorkflowNode interface {
	NodeID() NodeID
	workflowNode()
}

// TaskNode is the executable node form of a parsed task.
type TaskNode struct {
	ID           TaskID
	Description  string
	DependsOn    []NodeID
	Action       Action
	Condition    *Condition
	Runtime      RuntimeSelector
	Policies     TaskPolicies
	WritesState  string
	Loop         LoopID
	SystemPrompt Template
}

// LoopSpec describes a structural loop node.
type LoopSpec = LoopGroup

// WorkflowFragment is the body of a structural workflow node.
type WorkflowFragment struct {
	Nodes []TaskNode
}

// LoopNode is a structural node whose body contains executable task nodes.
type LoopNode struct {
	ID          LoopID
	Description string
	Spec        LoopSpec
	Body        WorkflowFragment
}

// WorkflowDefinition is an immutable view of a parsed workflow in node form.
type WorkflowDefinition struct {
	ID          WorkflowID
	Description string
	Defaults    WorkflowDefaults
	Params      []Param
	Nodes       []WorkflowNode
	Order       []TaskID
	Output      OutputSelector
	Policies    WorkflowPolicies
}

func (TaskNode) workflowNode() {}
func (LoopNode) workflowNode() {}

func (n TaskNode) NodeID() NodeID { return NodeID(n.ID) }
func (n LoopNode) NodeID() NodeID { return NodeID(n.ID) }

// Definition returns a copy of w in the explicit node model used by the
// compiler. Parsed workflows return the semantic definition produced at parse
// time; hand-built workflows fall back to deriving the same view from legacy
// task fields.
func (w *Workflow) Definition() WorkflowDefinition {
	if w == nil {
		return WorkflowDefinition{}
	}
	if w.hasDefinition {
		return cloneWorkflowDefinition(w.definition)
	}
	return buildDefinitionFromWorkflow(w)
}

func (w *Workflow) storeDefinition() {
	w.definition = buildDefinitionFromWorkflow(w)
	w.hasDefinition = true
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

func cloneWorkflowDefinition(def WorkflowDefinition) WorkflowDefinition {
	out := def
	out.Params = append([]Param(nil), def.Params...)
	out.Order = append([]TaskID(nil), def.Order...)
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
