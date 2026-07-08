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
	def := wf.Definition()
	loops := compileLoopsFromPlan(pl)
	p := &program{
		wf:       wf,
		order:    workflowOrder(pl.Order),
		nodes:    compileNodesFromDefinition(def, pl),
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

func compileNodesFromDefinition(def workflow.WorkflowDefinition, pl *plan.Plan) map[workflow.TaskID]*node {
	tasks := taskPayloadsFromDefinition(def)
	nodes := make(map[workflow.TaskID]*node, len(tasks))
	for _, step := range pl.Steps {
		t, ok := tasks[step.Name]
		if !ok {
			continue
		}
		nodes[t.ID] = &node{
			id:     t.ID,
			task:   t,
			deps:   workflowOrder(step.Deps),
			when:   step.When,
			action: step.Action,
			policy: step.Policy,
			op:     compileOpFromPlan(step.Action),
		}
	}
	return nodes
}

func taskPayloadsFromDefinition(def workflow.WorkflowDefinition) map[workflow.TaskID]workflow.Task {
	tasks := make(map[workflow.TaskID]workflow.Task)
	add := func(task workflow.TaskNode) {
		tasks[task.ID] = task.Task()
	}
	for _, node := range def.Nodes {
		switch n := node.(type) {
		case workflow.TaskNode:
			add(n)
		case workflow.LoopNode:
			for _, task := range n.Body.Nodes {
				add(task)
			}
		}
	}
	return tasks
}

func compileOpFromPlan(action plan.Action) op {
	switch action.(type) {
	case plan.AskModel:
		return promptOp{}
	case plan.RunCommand:
		return shellOp{}
	case plan.RunScript:
		return scriptOp{}
	case plan.CallWorkflow:
		return subWorkflowOp{}
	default:
		return invalidOp{}
	}
}

func compileLoopsFromPlan(pl *plan.Plan) []*loopProgram {
	root, ok := pl.Blocks[pl.Root]
	if !ok {
		return nil
	}
	loops := make([]*loopProgram, 0)
	for _, stepID := range root.Steps {
		step, ok := pl.Steps[stepID]
		if !ok {
			continue
		}
		switch action := step.Action.(type) {
		case plan.ForEach:
			loops = append(loops, compileLoopFromStep(action.Loop, step.Deps))
		case plan.Repeat:
			loops = append(loops, compileLoopFromStep(action.Loop, step.Deps))
		}
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
