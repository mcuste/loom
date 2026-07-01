package executor

import (
	"context"

	"github.com/mcuste/loom/pkg/workflow"
)

// program is executor-local IR compiled from a parsed workflow. It will become
// the handoff between workflow loading and execution, but phase 1 keeps it as a
// dormant structure so runtime behavior stays unchanged.
type program struct {
	wf       *workflow.Workflow
	order    []workflow.TaskID
	units    []unit
	memberOf map[workflow.TaskID]int
}

// unit is one schedulable top-level item in a compiled program. Loop members
// are intentionally not top-level units: their owning loop unit drives them.
type unit interface {
	run(context.Context, *interpreter, *runState) error
}

// taskUnit is one top-level task scheduled directly by the executor.
type taskUnit struct {
	id workflow.TaskID
}

// loopUnit is one top-level scoped loop. Its member tasks stay off the
// program's top-level unit list because the loop drives them as a group.
type loopUnit struct {
	index int
}

// interpreter is the future program runner that will evaluate a compiled
// program without changing the executor's public API.
type interpreter struct{}
