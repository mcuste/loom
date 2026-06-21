package executor

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// runState bundles the shared, mutex-guarded run state that every task
// goroutine touches: the aggregate report, the succeeded/skipped maps, the
// per-task gates, the guarding mutex, and the budget admission slot.
//
// All map fields and rep are read and written under mu, exactly as Run does
// today. budgetInFlight and budgetReady serialize budget-gated dispatch; they
// are unused when the workflow declares no budget.
//
// Bundling these into one value (rather than a sprawling parameter list) lets
// the per-task dispatch body live in runTask and be reused by a future loop
// driver.
type runState struct {
	rep            *Report
	succeeded      map[workflow.TaskID]bool
	skipped        map[workflow.TaskID]bool
	gates          map[workflow.TaskID]chan struct{}
	mu             *sync.Mutex
	budgetInFlight bool
	budgetReady    *sync.Cond
}

// runTask executes one task end to end: it waits for the task's dependency
// gates, substitutes placeholders, evaluates the `when:` guard, enforces the
// workflow budget gate, dispatches with retry (schema validation and cache
// lookup/save for LLM tasks), fires hooks, and on success records the
// output/usage and closes the task's gate.
//
// ctx is the errgroup-derived context: a non-nil error from runTask aborts the
// run by leaving the gate unclosed and letting the errgroup cancel ctx, which
// unblocks sibling goroutines at their gate waits. The returned error is
// already wrapped with the task id.
func runTask(ctx context.Context, wf *workflow.Workflow, t *workflow.Task, st *runState, hooks Hooks, opts Options) error {
	// runTask is the single dispatch path Run's scheduler invokes per task, so a
	// future loop driver can reuse identical semantics by calling it directly.
	baseDelay := opts.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = defaultRetryBaseDelay
	}

	for _, dep := range t.DependsOn {
		select {
		case <-st.gates[dep]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Evaluate the task's `when:` guard once its dependencies have
	// resolved. A false result skips the task: it produces empty output
	// and StatusSkipped, but still closes its gate so downstream tasks
	// proceed. Cond was compiled and validated at load time.
	if t.Cond != nil {
		st.mu.Lock()
		env := workflow.Env{
			Outputs:   maps.Clone(st.rep.Outputs),
			Succeeded: maps.Clone(st.succeeded),
			Skipped:   maps.Clone(st.skipped),
		}
		st.mu.Unlock()
		run, err := t.Cond.Eval(env)
		if err != nil {
			return fmt.Errorf("task %q: when: %w", t.ID, err)
		}
		if !run {
			res := TaskResult{TaskID: t.ID, Status: StatusSkipped}
			if hooks.OnFinish != nil {
				hooks.OnFinish(*t, res, nil)
			}
			st.mu.Lock()
			st.rep.Outputs[t.ID] = ""
			st.skipped[t.ID] = true
			st.rep.Tasks = append(st.rep.Tasks, res)
			st.mu.Unlock()
			close(st.gates[t.ID])
			return nil
		}
	}

	// Enforce the workflow cost budget BEFORE dispatching: once the
	// cumulative cost of already-completed tasks exceeds the limit, abort
	// rather than start another task. Spend strictly greater than the
	// limit is "exceeded"; landing exactly on it is allowed.
	if wf.Budget != nil {
		// Wait until no other budget-gated task is in flight, then check and
		// claim the slot under the same lock. This makes the check-then-commit
		// atomic: the in-flight task's cost is recorded (and the slot
		// released) before the next task is admitted, so concurrent subgraphs
		// cannot each read a stale spend and collectively overshoot the limit.
		st.mu.Lock()
		for st.budgetInFlight {
			st.budgetReady.Wait()
			// A wake may come from a sibling's cancellation rather than a slot
			// release; bail without claiming the slot so we never block g.Wait.
			if ctx.Err() != nil {
				st.mu.Unlock()
				return ctx.Err()
			}
		}
		spent := st.rep.Usage.TotalCostUSD
		if spent > wf.Budget.MaxCostUSD {
			// Wake peers so they re-evaluate and drain (each will also abort)
			// rather than block forever on the slot this goroutine never takes.
			st.budgetReady.Broadcast()
			st.mu.Unlock()
			return &BudgetExceededError{Limit: wf.Budget.MaxCostUSD, Spent: spent}
		}
		st.budgetInFlight = true
		st.mu.Unlock()
		// Release the slot once this task returns (after its cost is recorded
		// on the success path, or immediately on a dispatch error), waking the
		// next waiter.
		defer func() {
			st.mu.Lock()
			st.budgetInFlight = false
			st.budgetReady.Broadcast()
			st.mu.Unlock()
		}()
	}

	var (
		res    TaskResult
		runErr error
	)

	// A for_each task resolves its list and substitutes per instance
	// inside runForEach, so no single body is computed up front; a plain
	// task substitutes its body once here (mu guards rep.Outputs;
	// opts.Params is read-only after Run starts).
	if t.IsShell() {
		if hooks.OnStart != nil {
			hooks.OnStart(*t, "", "", "")
		}
		if t.IsForEach() {
			res, runErr = runForEach(ctx, t, st.mu, st.rep.Outputs, opts, baseDelay, nil, "", "", "")
		} else {
			st.mu.Lock()
			body := workflow.Substitute(t.Command, st.rep.Outputs, opts.Params, opts.State)
			st.mu.Unlock()
			res, runErr = runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
				return runShell(ctx, t, body)
			})
		}
	} else {
		rt, model, effort := wf.Effective(t)
		runner, ok := runtime.Lookup(rt)
		if !ok {
			return fmt.Errorf("task %q: runtime %q: %w", t.ID, rt, runtime.ErrUnknownRuntime)
		}
		sysPrompt := workflow.Substitute(wf.SystemPrompt, nil, opts.Params, opts.State)
		if hooks.OnStart != nil {
			hooks.OnStart(*t, rt, model, effort)
		}
		if t.IsForEach() {
			// Memoization keys on a single substituted prompt; a for_each task
			// has one body per instance, so caching it is unsupported. Surface
			// the unsupported combination rather than silently ignoring the
			// annotation.
			if opts.Cache != nil && wf.CacheEnabled(t) {
				return fmt.Errorf("task %q: cache is not supported on for_each tasks", t.ID)
			}
			res, runErr = runForEach(ctx, t, st.mu, st.rep.Outputs, opts, baseDelay, runner, model, effort, sysPrompt)
		} else {
			st.mu.Lock()
			body := workflow.Substitute(t.Prompt, st.rep.Outputs, opts.Params, opts.State)
			st.mu.Unlock()
			dispatch := func() (TaskResult, error) {
				return runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
					r, err := runLLM(ctx, t, body, runner, model, effort, sysPrompt)
					if err != nil {
						return r, err
					}
					return r, validateSchema(t, r.Output)
				})
			}
			if opts.Cache != nil && wf.CacheEnabled(t) {
				res, runErr = runCached(opts.Cache, t, rt, model, effort, sysPrompt, body, dispatch)
			} else {
				res, runErr = dispatch()
			}
		}
	}

	if hooks.OnFinish != nil {
		hooks.OnFinish(*t, res, runErr)
	}
	if runErr != nil {
		// A task error aborts the run: the gate is left unclosed, errgroup
		// cancels ctx, and downstream goroutines exit at their <-ctx.Done()
		// wait before ever reaching their own when: evaluation. Consequently
		// failed(id) cannot observe a runtime failure of id in a live run
		// (it is reachable-true only for a never-succeeded, never-skipped
		// disposition, which a future continue-on-error mode would produce).
		// TestRun_WhenFailedDepAbortsRun pins this behavior.
		return fmt.Errorf("task %q: %w", t.ID, runErr)
	}

	res.Status = StatusOK
	st.mu.Lock()
	st.rep.Outputs[t.ID] = res.Output
	st.succeeded[t.ID] = true
	st.rep.Tasks = append(st.rep.Tasks, res)
	st.rep.Usage.InputTokens += res.Usage.InputTokens
	st.rep.Usage.OutputTokens += res.Usage.OutputTokens
	st.rep.Usage.CacheReadTokens += res.Usage.CacheReadTokens
	st.rep.Usage.TotalCostUSD += res.Usage.TotalCostUSD
	st.mu.Unlock()

	close(st.gates[t.ID])
	return nil
}
