package executor

import "github.com/mcuste/loom/pkg/workflow"

// loopProgram is the compiled form of one scoped loop. It hoists loop-member
// and entry-dependency analysis out of runtime evaluation so every iteration
// reuses the same static loop shape.
type loopProgram struct {
	group     *workflow.LoopGroup
	members   []workflow.TaskID
	memberSet map[workflow.TaskID]bool
	entryDeps map[workflow.TaskID]bool
}

func compileLoop(wf *workflow.Workflow, lg *workflow.LoopGroup) *loopProgram {
	lp := &loopProgram{
		group:     lg,
		members:   append([]workflow.TaskID(nil), lg.Members...),
		memberSet: make(map[workflow.TaskID]bool, len(lg.Members)),
		entryDeps: make(map[workflow.TaskID]bool),
	}
	for _, member := range lp.members {
		lp.memberSet[member] = true
	}
	for _, member := range lp.members {
		task := wf.ByID(member)
		if task == nil {
			continue
		}
		for _, dep := range task.DependsOn {
			if !lp.memberSet[dep] {
				lp.entryDeps[dep] = true
			}
		}
	}
	if lg.Kind == workflow.LoopForEach {
		if ref, ok := workflow.ListSourceTaskRef(lg.ListSource); ok && !lp.memberSet[ref] {
			lp.entryDeps[ref] = true
		}
	}
	return lp
}
