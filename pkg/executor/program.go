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
	order    []workflow.TaskID
	nodes    map[workflow.TaskID]*node
	loops    []*loopProgram
	units    []unit
	memberOf map[workflow.TaskID]int
}

// node is one compiled task plus its interpreter-ready action and policy.
// The task value is a hook/report compatibility payload materialized from the
// semantic definition; executable behavior is read from the compiled plan
// action instead of re-derived from YAML-shaped Workflow.Tasks fields.
type node struct {
	id     workflow.TaskID
	task   workflow.Task
	deps   []workflow.TaskID
	when   *workflow.Condition
	action plan.Action
	policy plan.Policy
	op     op
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
