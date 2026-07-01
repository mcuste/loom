package executor

import (
	"sync"

	"github.com/mcuste/loom/pkg/workflow"
)

// frame is the interpreter's current execution scope. Phase 5 keeps it as an
// alias for runState so new interpreter-facing code can adopt the target
// vocabulary without renaming stable call sites yet.
type frame = runState

// store is the current frame's output/disposition state. It remains an alias
// for scopeState until a later phase makes the rename concrete.
type store = scopeState

func newReport(order []workflow.TaskID, opts Options) *Report {
	return &Report{
		Tasks:   make([]TaskResult, 0, len(order)),
		Outputs: make(map[workflow.TaskID]string, len(order)),
		Params:  opts.Params,
	}
}

// newRootFrame builds the root interpreter frame for one public Run call. The
// root frame owns the shared report and store for that run; derived sequential
// loop passes reuse both directly, while parallel for_each passes snapshot the
// store but still share the report's rows, usage, and budget gate. A
// sub-workflow runs through its own public Run call, so it gets an independent
// report and root store even when it inherits options like state, cache, and
// working directory from its parent.
func newRootFrame(wf *workflow.Workflow, rep *Report, order []workflow.TaskID, opts Options) *frame {
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

	return &frame{
		runShared: &runShared{
			rep: rep,
			scope: store{
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
