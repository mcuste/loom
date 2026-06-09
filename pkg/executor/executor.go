// Package executor orchestrates a parsed [workflow.Workflow] against the
// runtimes registered in the [runtime] package. It computes the deterministic
// execution order via [workflow.Workflow.Plan], substitutes each task's
// `{{id}}` placeholders with the outputs of upstream tasks, and dispatches one
// [runtime.Request] per task in sequence. Hooks let callers observe per-task
// progress without coupling the orchestrator to a particular output sink.
package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// TaskResult is the outcome of one task execution.
type TaskResult struct {
	TaskID  workflow.TaskID
	Output  string
	Usage   runtime.Usage
	Elapsed time.Duration
}

// Report is the aggregate outcome of a Run call. Tasks lists per-task results
// in execution order; Outputs is the same data keyed by id for placeholder
// lookups; Usage sums Usage across every task. On partial failure (Run returns
// non-nil error), the report contains the tasks that completed before the
// failure.
type Report struct {
	Tasks   []TaskResult
	Outputs map[workflow.TaskID]string
	Usage   runtime.Usage
}

// Hooks let callers observe per-task progress without owning an output sink.
// Either field may be nil. OnStart fires once per task before Run is called
// on the runtime; OnFinish fires after Run returns, regardless of success
// (err is nil on success).
type Hooks struct {
	OnStart  func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort)
	OnFinish func(t workflow.Task, res TaskResult, err error)
}

// Run executes wf's tasks in [workflow.Workflow.Plan] order. It looks each
// task's effective runtime up in the registry, substitutes `{{id}}`
// placeholders in the prompt with upstream outputs, calls Runtime.Run, and
// accumulates results into a Report.
//
// On the first task error, Run aborts and returns the partial report along
// with an error wrapping the task id. Context cancellation is honored between
// tasks and propagates into each Runtime.Run call.
func Run(ctx context.Context, wf *workflow.Workflow, hooks Hooks) (*Report, error) {
	order := wf.Plan()
	rep := &Report{
		Tasks:   make([]TaskResult, 0, len(order)),
		Outputs: make(map[workflow.TaskID]string, len(order)),
	}

	for _, tid := range order {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		t := wf.ByID(tid)
		rt, model, effort := wf.Effective(*t)

		runner, err := lookupRunner(rt)
		if err != nil {
			return rep, fmt.Errorf("task %q: %w", tid, err)
		}

		req := runtime.Request{
			TaskID:       string(tid),
			Prompt:       workflow.Substitute(t.Prompt, rep.Outputs),
			Model:        model,
			Effort:       effort,
			SystemPrompt: wf.SystemPrompt,
		}

		if hooks.OnStart != nil {
			hooks.OnStart(*t, rt, model, effort)
		}

		start := time.Now()
		resp, runErr := runner.Run(ctx, req)
		res := TaskResult{
			TaskID:  tid,
			Output:  resp.Output,
			Usage:   resp.Usage,
			Elapsed: time.Since(start),
		}

		if hooks.OnFinish != nil {
			hooks.OnFinish(*t, res, runErr)
		}
		if runErr != nil {
			return rep, fmt.Errorf("task %q: %w", tid, runErr)
		}

		rep.Tasks = append(rep.Tasks, res)
		rep.Outputs[tid] = resp.Output
		rep.Usage.InputTokens += resp.Usage.InputTokens
		rep.Usage.OutputTokens += resp.Usage.OutputTokens
		rep.Usage.CacheReadTokens += resp.Usage.CacheReadTokens
		rep.Usage.TotalCostUSD += resp.Usage.TotalCostUSD
	}
	return rep, nil
}

// lookupRunner resolves a runtime name to a [runtime.Runtime]. The registry
// stores values typed as [runtime.RuntimeSpec] (the validation surface); every
// runtime registered for execution must also implement Run.
func lookupRunner(name runtime.Name) (runtime.Runtime, error) {
	spec, ok := runtime.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("runtime %q: %w", name, runtime.ErrUnknownRuntime)
	}
	r, ok := spec.(runtime.Runtime)
	if !ok {
		return nil, fmt.Errorf("runtime %q: registered spec does not implement Run", name)
	}
	return r, nil
}
