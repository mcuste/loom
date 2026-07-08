package executor

import (
	"github.com/mcuste/loom/pkg/plan"
	"github.com/mcuste/loom/pkg/workflow"
)

// loopProgram is the compiled form of one scoped loop. It hoists loop-member
// and entry-dependency analysis out of runtime evaluation so every iteration
// reuses the same static loop shape.
type loopProgram struct {
	group     workflow.LoopGroup
	members   []workflow.TaskID
	memberSet map[workflow.TaskID]bool
	entryDeps map[workflow.TaskID]bool
}

func compileLoopFromStep(lg workflow.LoopGroup, deps []plan.StepID) *loopProgram {
	lg = cloneLoopGroup(lg)
	lp := &loopProgram{
		group:     lg,
		members:   append([]workflow.TaskID(nil), lg.Members...),
		memberSet: make(map[workflow.TaskID]bool, len(lg.Members)),
		entryDeps: make(map[workflow.TaskID]bool, len(deps)),
	}
	for _, member := range lp.members {
		lp.memberSet[member] = true
	}
	for _, dep := range deps {
		lp.entryDeps[workflow.TaskID(dep)] = true
	}
	return lp
}

func cloneLoopGroup(g workflow.LoopGroup) workflow.LoopGroup {
	g.List = append([]string(nil), g.List...)
	g.Members = append([]workflow.TaskID(nil), g.Members...)
	return g
}

// buildInnerGates allocates a fresh gate channel for each loop member and
// aliases the already-closed outer gates for each entry dependency into the
// same map, so inner frames can satisfy both member and external waits without
// any additional coordination.
func (lp *loopProgram) buildInnerGates(outer map[workflow.TaskID]chan struct{}) map[workflow.TaskID]chan struct{} {
	innerGates := make(map[workflow.TaskID]chan struct{}, len(lp.members)+len(lp.entryDeps))
	for _, m := range lp.members {
		innerGates[m] = make(chan struct{})
	}
	for dep := range lp.entryDeps {
		innerGates[dep] = outer[dep]
	}
	return innerGates
}
