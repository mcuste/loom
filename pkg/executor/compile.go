package executor

import "github.com/mcuste/loom/pkg/workflow"

func compileProgram(wf *workflow.Workflow) *program {
	loops := compileLoops(wf)
	p := &program{
		wf:       wf,
		order:    wf.Plan(),
		nodes:    compileNodes(wf),
		loops:    loops,
		memberOf: buildMemberOf(loops),
	}
	p.units = compileUnits(p.order, p.memberOf, p.loops)
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
