package workflow

import (
	"fmt"

	"github.com/mcuste/loom/pkg/runtime"
)

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
	When         string
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

// Definition is the parsed, validated semantic workflow model consumed by
// planners and executors. It is intentionally independent of the YAML syntax
// structs and of the legacy materialized Workflow view.
type Definition = WorkflowDefinition

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
	Schedule    *Schedule
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

func (w *Workflow) storeDefinition(def WorkflowDefinition) {
	w.definition = cloneWorkflowDefinition(def)
	w.hasDefinition = true
}

// TaskNodes returns the executable task nodes in declaration order. Loop body
// tasks are flattened into the same global task namespace as top-level tasks.
func (def WorkflowDefinition) TaskNodes() []TaskNode {
	tasks := definitionTaskNodes(def)
	out := make([]TaskNode, len(tasks))
	for i, task := range tasks {
		out[i] = cloneTaskNode(task)
	}
	return out
}

// TaskNode returns the executable task node named id.
func (def WorkflowDefinition) TaskNode(id TaskID) (TaskNode, bool) {
	for _, task := range definitionTaskNodes(def) {
		if task.ID == id {
			return cloneTaskNode(task), true
		}
	}
	return TaskNode{}, false
}

// HasTask reports whether id names an executable task in the definition.
func (def WorkflowDefinition) HasTask(id TaskID) bool {
	for _, task := range definitionTaskNodes(def) {
		if task.ID == id {
			return true
		}
	}
	return false
}

// OutputTask returns the task selected as this definition's result.
func (def WorkflowDefinition) OutputTask() (TaskID, error) {
	if def.Output.Task != "" {
		if !def.HasTask(def.Output.Task) {
			return "", &UnknownOutputTaskError{Task: def.Output.Task}
		}
		return def.Output.Task, nil
	}
	tasks := definitionTaskNodes(def)
	hasDependent := make(map[TaskID]bool, len(tasks))
	for _, task := range tasks {
		for _, dep := range task.DependsOn {
			hasDependent[TaskID(dep)] = true
		}
	}
	var sinks []TaskID
	for _, task := range tasks {
		if !hasDependent[task.ID] {
			sinks = append(sinks, task.ID)
		}
	}
	switch len(sinks) {
	case 1:
		return sinks[0], nil
	case 0:
		return "", fmt.Errorf("workflow %q: no terminal task to use as output, set output to pick one", def.ID)
	default:
		return "", fmt.Errorf("workflow %q: %d terminal tasks; set output: to pick one", def.ID, len(sinks))
	}
}

func definitionHasTask(def WorkflowDefinition, id TaskID) bool {
	return def.HasTask(id)
}

func definitionTaskNodes(def WorkflowDefinition) []TaskNode {
	var tasks []TaskNode
	for _, node := range def.Nodes {
		switch n := node.(type) {
		case TaskNode:
			tasks = append(tasks, n)
		case LoopNode:
			tasks = append(tasks, n.Body.Nodes...)
		}
	}
	return tasks
}
