package executor

import (
	"fmt"

	"github.com/mcuste/loom/pkg/plan"
	"github.com/mcuste/loom/pkg/workflow"
)

func compileProgram(wf *workflow.Workflow) (*program, error) {
	if wf == nil {
		return nil, fmt.Errorf("workflow is nil")
	}
	def := wf.Definition()
	pl, err := plan.CompileDefinition(def, plan.CompileOptions{})
	if err != nil {
		return nil, fmt.Errorf("compile plan: %w", err)
	}
	return compileProgramFromDefinition(wf, def, pl), nil
}

func compileProgramFromDefinition(wf *workflow.Workflow, def workflow.Definition, pl *plan.Plan) *program {
	loops := compileLoopsFromPlan(pl)
	p := &program{
		wf:       wf,
		def:      def,
		plan:     pl,
		nodes:    compileNodesFromDefinition(def, pl),
		loops:    loops,
		memberOf: buildMemberOf(loops),
	}
	p.units = compileUnits(p.order(), p.memberOf, p.loops)
	return p
}

func workflowOrder(order []plan.StepID) []workflow.TaskID {
	out := make([]workflow.TaskID, len(order))
	for i, id := range order {
		out[i] = workflow.TaskID(id)
	}
	return out
}

func compileNodesFromDefinition(def workflow.Definition, pl *plan.Plan) map[workflow.TaskID]*node {
	tasks := def.TaskNodes()
	nodes := make(map[workflow.TaskID]*node, len(tasks))
	for _, task := range tasks {
		step, ok := pl.Steps[plan.StepID(task.ID)]
		if !ok {
			continue
		}
		nodes[task.ID] = newNode(task, step)
	}
	return nodes
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
