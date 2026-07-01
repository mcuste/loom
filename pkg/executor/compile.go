package executor

import "github.com/mcuste/loom/pkg/workflow"

func compileProgram(wf *workflow.Workflow) *program {
	p := &program{
		wf:       wf,
		order:    wf.Plan(),
		memberOf: buildMemberOf(wf),
	}
	p.units = compileUnits(wf, p.order, p.memberOf)
	return p
}

func buildMemberOf(wf *workflow.Workflow) map[workflow.TaskID]int {
	memberOf := make(map[workflow.TaskID]int)
	for i := range wf.Loops {
		for _, member := range wf.Loops[i].Members {
			memberOf[member] = i
		}
	}
	return memberOf
}

func compileUnits(wf *workflow.Workflow, order []workflow.TaskID, memberOf map[workflow.TaskID]int) []unit {
	units := make([]unit, 0, len(wf.Loops)+len(order))
	for i := range wf.Loops {
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
