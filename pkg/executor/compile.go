package executor

import (
	"fmt"

	"github.com/mcuste/loom/pkg/plan"
	"github.com/mcuste/loom/pkg/workflow"
)

func compileProgram(wf *workflow.Workflow) *program {
	pl, err := plan.Compile(wf, plan.CompileOptions{})
	if err != nil {
		panic(fmt.Sprintf("executor compile: %v", err))
	}
	return compileProgramFromPlan(wf, pl)
}

func compileProgramFromPlan(wf *workflow.Workflow, pl *plan.Plan) *program {
	loops := compileLoops(wf)
	p := &program{
		wf:       wf,
		order:    workflowOrder(pl.Order),
		nodes:    compileNodesFromPlan(wf, pl),
		loops:    loops,
		memberOf: buildMemberOf(loops),
	}
	p.units = compileUnits(p.order, p.memberOf, p.loops)
	return p
}

func workflowOrder(order []plan.StepID) []workflow.TaskID {
	out := make([]workflow.TaskID, len(order))
	for i, id := range order {
		out[i] = workflow.TaskID(id)
	}
	return out
}

func compileNodes(wf *workflow.Workflow) map[workflow.TaskID]*node {
	pl, err := plan.Compile(wf, plan.CompileOptions{})
	if err != nil {
		panic(fmt.Sprintf("executor compile nodes: %v", err))
	}
	return compileNodesFromPlan(wf, pl)
}

func compileNodesFromPlan(wf *workflow.Workflow, pl *plan.Plan) map[workflow.TaskID]*node {
	nodes := make(map[workflow.TaskID]*node, len(wf.Tasks))
	for _, step := range pl.Steps {
		t := wf.ByID(step.Name)
		if t == nil {
			continue
		}
		nodes[t.ID] = &node{
			id:     t.ID,
			task:   t,
			deps:   workflowOrder(step.Deps),
			when:   step.When,
			action: step.Action,
			policy: step.Policy,
			op:     compileOpFromPlan(step.Action, t),
		}
	}
	return nodes
}

func compileOpFromPlan(action plan.Action, t *workflow.Task) op {
	switch action.(type) {
	case plan.AskModel:
		return promptOp{}
	case plan.RunCommand:
		return shellOp{}
	case plan.RunScript:
		return scriptOp{}
	case plan.CallWorkflow:
		return subWorkflowOp{}
	case plan.InvalidAction:
		return invalidOp{}
	default:
		return compileOp(t)
	}
}

func compileLoops(wf *workflow.Workflow) []*loopProgram {
	loops := make([]*loopProgram, 0, len(wf.Loops))
	for i := range wf.Loops {
		loops = append(loops, compileLoop(wf, &wf.Loops[i]))
	}
	return loops
}

func buildMemberOf(loops []*loopProgram) map[workflow.TaskID]int {
	memberOf := make(map[workflow.TaskID]int)
	for i, loop := range loops {
		for _, member := range loop.members {
			memberOf[member] = i
		}
	}
	return memberOf
}

func compileUnits(order []workflow.TaskID, memberOf map[workflow.TaskID]int, loops []*loopProgram) []unit {
	units := make([]unit, 0, len(loops)+len(order))
	for i := range loops {
		units = append(units, loopUnit{index: i})
	}
	for _, id := range order {
		if _, loopMember := memberOf[id]; loopMember {
			continue
		}
		units = append(units, taskUnit{id: id})
	}
	return units
}
