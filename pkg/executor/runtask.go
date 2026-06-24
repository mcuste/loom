package executor

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// bindLoopVar replaces a for_each member's `{{loopVar}}` placeholder with the
// current iteration's element before the normal placeholder substitution runs.
// It is a no-op outside a for_each loop (st.loopVar == ""), where the binding
// is empty and the body passes through unchanged.
func bindLoopVar(body string, st *runState) string {
	if st.loopVar == "" {
		return body
	}
	return strings.ReplaceAll(body, "{{"+st.loopVar+"}}", st.loopVal)
}

// runState bundles the shared, mutex-guarded run state that every task
// goroutine touches: the aggregate report, the succeeded/skipped maps, the
// per-task gates, the guarding mutex, and the budget admission slot.
//
// All map fields and rep are read and written under mu, exactly as Run does
// today. budgetInFlight and budgetReady serialize budget-gated dispatch; they
// are unused when the workflow declares no budget.
//
// Bundling these into one value (rather than a sprawling parameter list) lets
// the per-task dispatch body live in runTask and be reused by runLoop.
type runState struct {
	rep       *Report
	succeeded map[workflow.TaskID]bool
	skipped   map[workflow.TaskID]bool
	gates     map[workflow.TaskID]chan struct{}
	mu        *sync.Mutex
	// budgetInFlight is a pointer so a scoped-loop iteration's derived runState
	// (fresh gates, but the same report, mutex, and budget slot) serializes
	// budget-gated dispatch against the outer schedule rather than against a
	// private copy of the flag.
	budgetInFlight *bool
	budgetReady    *sync.Cond
	// prev maps a loop member id to its prior-iteration output, consulted for
	// `{{prev.id}}` substitution inside a scoped-loop body. nil outside a loop
	// (and on the first iteration), where prev placeholders collapse to empty.
	prev map[workflow.TaskID]string
	// iteration is the 1-based loop pass stamped onto every result produced in
	// this scope, and 0 outside a scoped loop.
	iteration int
	// loopVar is the for_each loop-variable name (the loop's `as`) bound for this
	// iteration, and "" outside a for_each loop. When set, runTask replaces the
	// `{{loopVar}}` placeholder in a member body with loopVal before the normal
	// substitution.
	loopVar string
	// loopVal is the current element bound to loopVar for this iteration.
	loopVal string
}

// forLoopIteration derives a runState for one scoped-loop pass: it shares the
// report, succeeded/skipped maps, mutex, and budget slot with st (so usage and
// budget accounting stay global) but swaps in a fresh per-iteration gate set,
// the prior iteration's outputs for prev substitution, the 1-based pass number
// stamped onto results, and the for_each loop-variable binding (loopVar/loopVal,
// both "" for a while loop).
func (st *runState) forLoopIteration(gates map[workflow.TaskID]chan struct{}, prev map[workflow.TaskID]string, iteration int, loopVar, loopVal string) *runState {
	inner := *st
	inner.gates = gates
	inner.prev = prev
	inner.iteration = iteration
	inner.loopVar = loopVar
	inner.loopVal = loopVal
	return &inner
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
			res := TaskResult{TaskID: t.ID, Status: StatusSkipped, Iteration: st.iteration}
			if hooks.OnFinish != nil {
				hooks.OnFinish(*t, st.iteration, res, nil)
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
		for *st.budgetInFlight {
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
		*st.budgetInFlight = true
		st.mu.Unlock()
		// Release the slot once this task returns (after its cost is recorded
		// on the success path, or immediately on a dispatch error), waking the
		// next waiter.
		defer func() {
			st.mu.Lock()
			*st.budgetInFlight = false
			st.budgetReady.Broadcast()
			st.mu.Unlock()
		}()
	}

	var (
		res    TaskResult
		runErr error
	)

	// Each task substitutes its body once here (mu guards rep.Outputs; opts.Params
	// is read-only after Run starts). A for_each member additionally binds its
	// loop variable to the current element first, so a {{loopVar}} value
	// containing placeholder-looking text is spliced before (not re-expanded
	// across) the normal substitution.
	switch {
	case t.IsSubWorkflow():
		// A sub-workflow task is a leaf in this DAG: at dispatch it recursively
		// runs the linked child via Run and captures its result. The child brings
		// its own runtime, so there is no runtime.Lookup here (like a shell task).
		child := opts.Subs[t.ID]
		if child == nil {
			return fmt.Errorf("task %q: sub-workflow %q not linked", t.ID, t.Workflow)
		}
		if hooks.OnStart != nil {
			hooks.OnStart(*t, st.iteration, "", "", "")
		}
		// with-values are substituted against the PARENT context first, then
		// handed to the child as its CLI-tier param values.
		st.mu.Lock()
		vals := make(map[string]string, len(t.With))
		for _, a := range t.With {
			vals[string(a.Name)] = workflow.Substitute(a.Value, st.rep.Outputs, opts.Params, opts.State, st.prev)
		}
		st.mu.Unlock()
		res, runErr = runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
			start := time.Now()
			cp, err := workflow.ResolveParams(child, vals, nil)
			if err != nil {
				return TaskResult{TaskID: t.ID}, err
			}
			childRep, err := Run(ctx, child, Hooks{}, Options{
				Params: cp,
				// Child shares the parent's cross-run state so {{state.x}} placeholders in
				// child prompts resolve the same store. Write-back (writes_state) is a
				// CLI-layer pass over the top-level report only, so the child never mutates
				// the map here.
				State:          opts.State,
				Subs:           child.Subs,
				Cache:          opts.Cache,
				RetryBaseDelay: opts.RetryBaseDelay,
			})
			if err != nil {
				return TaskResult{TaskID: t.ID}, err
			}
			ot, err := child.OutputTask()
			if err != nil {
				return TaskResult{TaskID: t.ID}, err
			}
			out := childRep.Outputs[ot]
			// One parent row, child result + SUMMED child usage; schema (if any)
			// validates the child result uniformly with the LLM branch.
			r := TaskResult{TaskID: t.ID, Output: out, Usage: childRep.Usage, Elapsed: time.Since(start)}
			return r, validateSchema(t, out)
		})
	case t.IsShell():
		if hooks.OnStart != nil {
			hooks.OnStart(*t, st.iteration, "", "", "")
		}
		st.mu.Lock()
		body := workflow.Substitute(bindLoopVar(t.Command, st), st.rep.Outputs, opts.Params, opts.State, st.prev)
		st.mu.Unlock()
		res, runErr = runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
			return runShell(ctx, t, body)
		})
	default:
		rt, model, effort := wf.Effective(t)
		runner, ok := runtime.Lookup(rt)
		if !ok {
			return fmt.Errorf("task %q: runtime %q: %w", t.ID, rt, runtime.ErrUnknownRuntime)
		}
		sysPrompt := workflow.Substitute(wf.SystemPrompt, nil, opts.Params, opts.State, nil)
		if hooks.OnStart != nil {
			hooks.OnStart(*t, st.iteration, rt, model, effort)
		}
		st.mu.Lock()
		body := workflow.Substitute(bindLoopVar(t.Prompt, st), st.rep.Outputs, opts.Params, opts.State, st.prev)
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

	// Stamp the iteration before firing OnFinish so an observer reading
	// res.Iteration inside the hook sees the loop pass, not a stale 0; the
	// invariant on TaskResult.Iteration must already hold when the hook runs.
	res.Iteration = st.iteration
	if hooks.OnFinish != nil {
		hooks.OnFinish(*t, st.iteration, res, runErr)
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
