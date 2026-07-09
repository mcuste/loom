package executor

import (
	"context"

	"github.com/mcuste/loom/pkg/plan"
	"github.com/mcuste/loom/pkg/workflow"
)

// program is executor-local IR compiled from a parsed workflow. It carries the
// deterministic schedule metadata the executor needs before interpretation.
type program struct {
	wf       *workflow.Workflow
	plan     *plan.Plan
	nodes    map[workflow.TaskID]*node
	loops    []*loopProgram
	units    []unit
	memberOf map[workflow.TaskID]int
}

func (p *program) order() []workflow.TaskID {
	if p == nil || p.plan == nil {
		return nil
	}
	return workflowOrder(p.plan.Order)
}

// node is one compiled task plus its interpreter-ready operation. The task
// value is a hook/report compatibility payload materialized from the semantic
// definition; executable behavior and policy stay in the compiled plan step.
type node struct {
	task workflow.Task
	step plan.Step
	op   op
}

func (n *node) id() workflow.TaskID {
	if n == nil {
		return ""
	}
	return n.task.ID
}

func (n *node) deps() []workflow.TaskID {
	if n == nil {
		return nil
	}
	return workflowOrder(n.step.Deps)
}

func (n *node) when() *workflow.Condition {
	if n == nil {
		return nil
	}
	return n.step.When
}

func (n *node) action() plan.Action {
	if n == nil {
		return plan.InvalidAction{}
	}
	return n.step.Action
}

func (n *node) policy() plan.Policy {
	if n == nil {
		return plan.Policy{}
	}
	return n.step.Policy
}

// unit is one schedulable top-level item in a compiled program. Loop members
// are intentionally not top-level units: their owning loop unit drives them.
type unit interface {
	run(context.Context, *interpreter, *frame) error
}

// taskUnit is one top-level task scheduled directly by the executor.
type taskUnit struct {
	id workflow.TaskID
}

// loopUnit is one top-level scoped loop. Its member tasks stay off the
// program's top-level unit list because the loop drives them as a group.
type loopUnit struct {
	index int
}
