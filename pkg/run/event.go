// Package run defines the execution event stream used by runner, store, and UI
// adapters.
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

// GatePoint identifies where a policy gate ran.
type GatePoint string

const (
	GatePreRun   GatePoint = "pre-run"
	GatePreStep  GatePoint = "pre-step"
	GatePostStep GatePoint = "post-step"
	GatePostRun  GatePoint = "post-run"
)

// BudgetScope names the scope a budget event applies to.
type BudgetScope string

// Usage aliases runtime usage while ledger types settle.
type Usage = runtime.Usage

// RunStarted reports creation of a run.
type RunStarted struct {
	RunID      string
	WorkflowID workflow.WorkflowID
}

// StepReady reports that one task's dependencies have all completed.
type StepReady struct {
	ID        workflow.TaskID
	Task      workflow.Task
	Iteration int
}

// GateEvaluated reports a policy gate decision.
type GateEvaluated struct {
	Point     GatePoint
	ID        workflow.TaskID
	Task      workflow.Task
	Allowed   bool
	Skipped   bool
	Reason    string
	Iteration int
}

// StepStarted reports dispatch of one task or loop iteration member.
type StepStarted struct {
	ID        workflow.TaskID
	Task      workflow.Task
	Iteration int
	Runtime   runtime.Name
	Model     runtime.Model
	Effort    runtime.Effort
}

// StepFinished reports a terminal task result.
type StepFinished struct {
	ID        workflow.TaskID
	Task      workflow.Task
	Iteration int
	Result    executor.TaskResult
	Err       error
}

// StepSkipped reports a when-guarded task that did not run.
type StepSkipped struct {
	ID        workflow.TaskID
	Task      workflow.Task
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

// UsageAccrued reports usage added to the run ledger.
type UsageAccrued struct {
	ID        workflow.TaskID
	Task      workflow.Task
	Iteration int
	Usage     runtime.Usage
}

// RunFinished reports completion of a run.
type RunFinished struct {
	Status string
	Err    error
}

func (RunStarted) event()     {}
func (StepReady) event()      {}
func (GateEvaluated) event()  {}
func (StepStarted) event()    {}
func (StepFinished) event()   {}
func (StepSkipped) event()    {}
func (BudgetReserved) event() {}
func (BudgetAccrued) event()  {}
func (UsageAccrued) event()   {}
func (RunFinished) event()    {}

// HooksFromSink adapts the event stream to executor hooks while the executor
// still drives task progress through its stable callback API.
func HooksFromSink(sink EventSink) executor.Hooks {
	if sink == nil {
		return executor.Hooks{}
	}
	return executor.Hooks{
		OnStart: func(t workflow.Task, iter int, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			sink.Emit(StepStarted{
				ID:        t.ID,
				Task:      t,
				Iteration: iter,
				Runtime:   rt,
				Model:     m,
				Effort:    e,
			})
		},
		OnFinish: func(t workflow.Task, iter int, res executor.TaskResult, err error) {
			if res.Status == task.StatusSkipped {
				sink.Emit(StepSkipped{ID: t.ID, Task: t, Iteration: iter})
			}
			sink.Emit(StepFinished{ID: t.ID, Task: t, Iteration: iter, Result: res, Err: err})
			if res.Usage != (runtime.Usage{}) {
				sink.Emit(UsageAccrued{ID: t.ID, Task: t, Iteration: iter, Usage: res.Usage})
			}
		},
	}
}

// JoinSinks fans each event out to every non-nil sink in order.
func JoinSinks(sinks ...EventSink) EventSink {
	return EventSinkFunc(func(e Event) {
		for _, sink := range sinks {
			if sink != nil {
				sink.Emit(e)
			}
		}
	})
}

// SinkFromHooks adapts event consumers back to executor hooks for renderers and
// recorders that have not yet moved their internal implementation to events.
func SinkFromHooks(hooks executor.Hooks) EventSink {
	return EventSinkFunc(func(e Event) {
		switch ev := e.(type) {
		case StepStarted:
			if hooks.OnStart != nil {
				hooks.OnStart(ev.Task, ev.Iteration, ev.Runtime, ev.Model, ev.Effort)
			}
		case StepFinished:
			if hooks.OnFinish != nil {
				hooks.OnFinish(ev.Task, ev.Iteration, ev.Result, ev.Err)
			}
		}
	})
}
