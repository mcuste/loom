package executor

import "github.com/mcuste/loom/pkg/workflow"

func compileProgram(wf *workflow.Workflow) *program {
	p := &program{
		wf:       wf,
		order:    wf.Plan(),
		nodes:    compileNodes(wf),
		memberOf: buildMemberOf(wf),
	}
	p.units = compileUnits(wf, p.order, p.memberOf)
	return p
}

func compileNodes(wf *workflow.Workflow) map[workflow.TaskID]*node {
	nodes := make(map[workflow.TaskID]*node, len(wf.Tasks))
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		nodes[t.ID] = &node{
			id:   t.ID,
			task: t,
			deps: append([]workflow.TaskID(nil), t.DependsOn...),
			op:   compileOp(t),
		}
	}
	return nodes
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
