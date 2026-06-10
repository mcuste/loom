// Package executor orchestrates a parsed [workflow.Workflow] against the
// runtimes registered in the [runtime] package. It computes the deterministic
// execution order via [workflow.Workflow.Plan], substitutes each task's
// `{{id}}` placeholders with the outputs of upstream tasks, and dispatches
// independent tasks concurrently against their registered runtimes. Hooks
// let callers observe per-task progress without coupling the orchestrator to
// a particular output sink.
package executor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// TaskResult is the outcome of one task execution.
//
// Prompt is the final text sent to the runtime, with `{{id}}` placeholders
// already substituted; persisting it lets callers reconstruct exactly what
// the model saw without re-running substitution.
type TaskResult struct {
	TaskID  workflow.TaskID
	Prompt  string
	Output  string
	Usage   runtime.Usage
	Elapsed time.Duration
}

// Report is the aggregate outcome of a Run call.
//
// Tasks lists per-task results in completion order — not Plan order — because
// independent tasks run concurrently and finish at different times. Outputs is
// the same data keyed by id for placeholder lookups. Usage sums Usage across
// every completed task. On partial failure (Run returns non-nil error), the
// report contains the tasks that completed before the failure.
type Report struct {
	Tasks   []TaskResult
	Outputs map[workflow.TaskID]string
	Usage   runtime.Usage
}

// Hooks let callers observe per-task progress without owning an output sink.
// Either field may be nil. OnStart fires once per task before Run is called
// on the runtime; OnFinish fires after Run returns, regardless of success
// (err is nil on success). Under concurrent execution, hook calls for
// different tasks may interleave; implementations must be safe for
// concurrent use.
type Hooks struct {
	OnStart  func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort)
	OnFinish func(t workflow.Task, res TaskResult, err error)
}

// Run executes wf's tasks concurrently, respecting the dependency graph.
// Each task waits for its upstream dependencies to complete, substitutes
// `{{id}}` placeholders in its prompt with their outputs, then dispatches a
// single [runtime.Request] to its registered runtime.
//
// On the first task error, sibling goroutines are cancelled via context and
// Run returns the partial Report along with the wrapped error. Cancellation
// of the caller's ctx propagates the same way.
func Run(ctx context.Context, wf *workflow.Workflow, hooks Hooks) (*Report, error) {
	order := wf.Plan()
	rep := &Report{
		Tasks:   make([]TaskResult, 0, len(order)),
		Outputs: make(map[workflow.TaskID]string, len(order)),
	}

	gates := make(map[workflow.TaskID]chan struct{}, len(order))
	for _, tid := range order {
		gates[tid] = make(chan struct{})
	}

	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	for _, tid := range order {
		t := wf.ByID(tid)
		g.Go(func() error {
			for _, dep := range t.DependsOn {
				select {
				case <-gates[dep]:
				case <-gctx.Done():
					return gctx.Err()
				}
			}

			rt, model, effort := wf.Effective(t)
			runner, ok := runtime.Lookup(rt)
			if !ok {
				return fmt.Errorf("task %q: runtime %q: %w", t.ID, rt, runtime.ErrUnknownRuntime)
			}

			mu.Lock()
			prompt := workflow.Substitute(t.Prompt, rep.Outputs)
			mu.Unlock()

			req := runtime.Request{
				TaskID:       string(t.ID),
				Prompt:       prompt,
				Model:        model,
				Effort:       effort,
				SystemPrompt: wf.SystemPrompt,
			}

			if hooks.OnStart != nil {
				hooks.OnStart(*t, rt, model, effort)
			}

			start := time.Now()
			resp, runErr := runner.Run(gctx, req)
			res := TaskResult{
				TaskID:  t.ID,
				Prompt:  prompt,
				Output:  resp.Output,
				Usage:   resp.Usage,
				Elapsed: time.Since(start),
			}

			if hooks.OnFinish != nil {
				hooks.OnFinish(*t, res, runErr)
			}
			if runErr != nil {
				return fmt.Errorf("task %q: %w", t.ID, runErr)
			}

			mu.Lock()
			rep.Outputs[t.ID] = resp.Output
			rep.Tasks = append(rep.Tasks, res)
			rep.Usage.InputTokens += resp.Usage.InputTokens
			rep.Usage.OutputTokens += resp.Usage.OutputTokens
			rep.Usage.CacheReadTokens += resp.Usage.CacheReadTokens
			rep.Usage.TotalCostUSD += resp.Usage.TotalCostUSD
			mu.Unlock()

			close(gates[t.ID])
			return nil
		})
	}

	err := g.Wait()
	return rep, err
}

// JoinHooks fans an event out to every hook set in registration order, so
// independent observers (printer, store, telemetry) can be layered without
// coupling their implementations. Nil function fields in any set are skipped.
func JoinHooks(hs ...Hooks) Hooks {
	return Hooks{
		OnStart: func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			for _, h := range hs {
				if h.OnStart != nil {
					h.OnStart(t, rt, m, e)
				}
			}
		},
		OnFinish: func(t workflow.Task, res TaskResult, err error) {
			for _, h := range hs {
				if h.OnFinish != nil {
					h.OnFinish(t, res, err)
				}
			}
		},
	}
}
