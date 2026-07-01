// Package run defines the execution event stream used by future runner, store,
// and UI adapters.
package run

import (
	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/task"
	"github.com/mcuste/loom/pkg/workflow"
)

// Event is one execution lifecycle fact.
type Event interface {
	event()
}

// EventSink receives execution events.
type EventSink interface {
	Emit(Event)
}

// EventSinkFunc adapts a function into an EventSink.
type EventSinkFunc func(Event)

// Emit calls f with e.
func (f EventSinkFunc) Emit(e Event) {
	f(e)
}

// BudgetScope names the scope a budget event applies to.
type BudgetScope string

// Usage aliases runtime usage while ledger types settle.
type Usage = runtime.Usage

// RunStarted reports creation of a run.
type RunStarted struct {
	RunID      string
	WorkflowID workflow.WorkflowID
}

// StepStarted reports dispatch of one task or loop iteration member.
type StepStarted struct {
	ID        workflow.TaskID
	Iteration int
	Runtime   runtime.Name
	Model     runtime.Model
	Effort    runtime.Effort
}

// StepFinished reports a terminal task result.
type StepFinished struct {
	ID     workflow.TaskID
	Result executor.TaskResult
	Err    error
}

// StepSkipped reports a when-guarded task that did not run.
type StepSkipped struct {
	ID        workflow.TaskID
	Iteration int
}

// BudgetReserved reports an estimated budget reservation.
type BudgetReserved struct {
	Scope    BudgetScope
	Estimate Usage
}

// BudgetAccrued reports actual usage after an effect completes.
type BudgetAccrued struct {
	Scope BudgetScope
	Usage runtime.Usage
}

// RunFinished reports completion of a run.
type RunFinished struct {
	Status string
	Err    error
}

func (RunStarted) event()     {}
func (StepStarted) event()    {}
func (StepFinished) event()   {}
func (StepSkipped) event()    {}
func (BudgetReserved) event() {}
func (BudgetAccrued) event()  {}
func (RunFinished) event()    {}

// HooksFromSink adapts the event stream to the legacy executor hooks.
func HooksFromSink(sink EventSink) executor.Hooks {
	if sink == nil {
		return executor.Hooks{}
	}
	return executor.Hooks{
		OnStart: func(t workflow.Task, iter int, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			sink.Emit(StepStarted{
				ID:        t.ID,
				Iteration: iter,
				Runtime:   rt,
				Model:     m,
				Effort:    e,
			})
		},
		OnFinish: func(t workflow.Task, iter int, res executor.TaskResult, err error) {
			if res.Status == task.StatusSkipped {
				sink.Emit(StepSkipped{ID: t.ID, Iteration: iter})
			}
			sink.Emit(StepFinished{ID: t.ID, Result: res, Err: err})
		},
	}
}
