package executor

import (
	"context"
	"fmt"

	"github.com/mcuste/loom/pkg/workflow"
)

// program is executor-local IR compiled from a parsed workflow. It carries the
// deterministic schedule metadata the executor needs before interpretation.
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

func (u taskUnit) run(context.Context, *interpreter, *runState) error {
	return fmt.Errorf("task unit %q has no interpreter runner", u.id)
}

func (u loopUnit) run(context.Context, *interpreter, *runState) error {
	return fmt.Errorf("loop unit %d has no interpreter runner", u.index)
}

// interpreter is the future program runner that will evaluate a compiled
// program without changing the executor's public API.
type interpreter struct{}
