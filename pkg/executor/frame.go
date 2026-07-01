package executor

import (
	"sync"

	"github.com/mcuste/loom/pkg/workflow"
)

func newReport(order []workflow.TaskID, opts Options) *Report {
	return &Report{
		Tasks:   make([]TaskResult, 0, len(order)),
		Outputs: make(map[workflow.TaskID]string, len(order)),
		Params:  opts.Params,
	}
}

func newRootFrame(wf *workflow.Workflow, rep *Report, order []workflow.TaskID, opts Options) *runState {
	gates := make(map[workflow.TaskID]chan struct{}, len(order))
	for _, tid := range order {
		gates[tid] = make(chan struct{})
	}

	succeeded := make(map[workflow.TaskID]bool, len(order))
	skipped := make(map[workflow.TaskID]bool, len(order))
	exitCodes := make(map[workflow.TaskID]int, len(order))

	for _, tid := range order {
		if v, ok := opts.Seed[tid]; ok {
			rep.Outputs[tid] = v
			succeeded[tid] = true
			if code, ok := opts.SeedExitCodes[tid]; ok {
				exitCodes[tid] = code
			}
			close(gates[tid])
		}
	}

	var mu sync.Mutex
	workDir := opts.WorkDir
	if wf.WorkingDir != "" {
		workDir = wf.WorkingDir
	}

	return &runState{
		runShared: &runShared{
			rep: rep,
			scope: scopeState{
				outputs:   rep.Outputs,
				succeeded: succeeded,
				skipped:   skipped,
				exitCodes: exitCodes,
			},
			mu:      &mu,
			budget:  &budgetGate{ready: sync.NewCond(&mu)},
			workDir: workDir,
		},
		loopCtx: loopCtx{
			gates: gates,
		},
	}
}
