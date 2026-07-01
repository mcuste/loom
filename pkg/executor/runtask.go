package executor

import (
	"context"
	"fmt"
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

// runShared holds the run-global invariant state shared across all iterations.
// Every field is read or written under mu, except workDir (immutable after init).
type runShared struct {
	rep *Report
	// scope is the current frame store: the four run-scope maps (outputs,
	// succeeded, skipped, exitCodes) that are always cloned and merged together.
	// outputs aliases
	// rep.Outputs for the top-level schedule, sequential loops, and while loops,
	// so those paths publish straight into the report. A parallel for_each
	// iteration swaps in a private copy (seeded from a snapshot of the shared
	// scope) so concurrent passes neither observe nor clobber one another's
	// member results; the driver merges each pass's scope back afterward.
	scope scopeState
	mu    *sync.Mutex
	// budget is a pointer so a scoped-loop iteration's derived runState (fresh
	// gates, but the same report, mutex, and budget slot) serializes
	// budget-gated dispatch against the outer schedule rather than against a
	// private copy of the gate.
	budget *budgetGate
	// workDir is the cwd every task process in this run is launched in (the LLM
	// runtime, shell, and script alike), resolved once in Run from the workflow's
	// working_dir. "" inherits loom's process cwd. Immutable after init.
	workDir string
}

// loopCtx is the per-pass context carried by value to each goroutine.
// None of its fields require mu: they are either immutable per-pass (iteration,
// loopVar, loopVal, prev) or concurrency-safe by design (gates: channels, closed
// once, never re-opened).
type loopCtx struct {
	gates map[workflow.TaskID]chan struct{}
	// prev maps a loop member id to its prior-iteration output, consulted for
	// `{{prev.id}}` substitution inside a scoped-loop body. nil outside a loop
	// (and on the first iteration), where prev placeholders collapse to empty.
	prev map[workflow.TaskID]string
	// iteration is the 1-based loop pass stamped onto every result produced in
	// this scope, and 0 outside a scoped loop.
	iteration int
	// loopVar is the for_each loop-variable name (the loop's `as`) bound for this
	// iteration, and "" outside a for_each loop. When set, task evaluation
	// replaces the `{{loopVar}}` placeholder in a member body with loopVal
	// before the normal substitution.
	loopVar string
	// loopVal is the current element bound to loopVar for this iteration.
	loopVal string
}

// runState is the current interpreter frame. It combines a pointer to the
// shared run invariants with the per-pass context so executor helpers get
// direct field access (st.mu, st.scope, st.gates, st.iteration) without
// changing stable call sites.
type runState struct {
	*runShared
	loopCtx
}

// forLoopIteration derives a runState for one scoped-loop pass: it shares the
// report, succeeded/skipped maps, mutex, and budget slot with st (so usage and
// budget accounting stay global) but swaps in a fresh per-iteration gate set,
// the prior iteration's outputs for prev substitution, the 1-based pass number
// stamped onto results, and the for_each loop-variable binding (loopVar/loopVal,
// both "" for a while loop).
func (st *runState) forLoopIteration(gates map[workflow.TaskID]chan struct{}, prev map[workflow.TaskID]string, iteration int, loopVar, loopVal string) *runState {
	return &runState{
		runShared: st.runShared,
		loopCtx: loopCtx{
			gates:     gates,
			prev:      prev,
			iteration: iteration,
			loopVar:   loopVar,
			loopVal:   loopVal,
		},
	}
}

// forParallelIteration derives a runState for one pass of a parallel for_each.
// Unlike forLoopIteration it swaps in private outputs/succeeded/skipped maps,
// each a snapshot of the shared state taken under st.mu, so concurrent passes
// neither read nor overwrite one another's member results: an item's sub-DAG
// sees only entry-dependency outputs plus its own members. The shared report
// (rows, usage, budget slot) and mutex stay shared; the driver merges each
// pass's member outputs back afterward. prev is nil: a parallel body has no
// prior iteration to read (the parser rejects {{prev.id}} inside one).
func (st *runState) forParallelIteration(gates map[workflow.TaskID]chan struct{}, iteration int, loopVar, loopVal string) *runState {
	sh := &runShared{
		rep:     st.rep,
		scope:   st.scope.cloneUnderLock(st.mu),
		mu:      st.mu,
		budget:  st.budget,
		workDir: st.workDir,
	}
	return &runState{
		runShared: sh,
		loopCtx: loopCtx{
			gates:     gates,
			prev:      nil,
			iteration: iteration,
			loopVar:   loopVar,
			loopVal:   loopVal,
		},
	}
}

// waitDeps blocks until every dependency gate has closed, or returns ctx.Err
// if the run is cancelled (a sibling failed) before the deps resolve.
func (st *runState) waitDeps(ctx context.Context, deps []workflow.TaskID) error {
	for _, dep := range deps {
		select {
		case <-st.gates[dep]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// evalWhen evaluates t's compiled `when:` guard against a snapshot of the
// current outputs/succeeded/skipped state and reports whether the task should
// run. The caller guarantees t.Cond != nil; Cond was compiled at load time.
func (st *runState) evalWhen(t *workflow.Task) (bool, error) {
	env := st.scope.snapshotEnv(st.mu)
	return t.Cond.Eval(env)
}

// admitBudget delegates to the run's [budgetGate], passing a closure that
// reads the cumulative cost under the gate's lock. See [budgetGate.admit] for
// the full serialization protocol.
func (st *runState) admitBudget(ctx context.Context, wf *workflow.Workflow) (func(), error) {
	return st.budget.admit(ctx, func() float64 {
		return st.rep.Usage.TotalCostUSD
	}, wf.Budget.MaxCostUSD)
}

// recordSkip publishes a `when:`-guarded task that was skipped: it fires
// OnFinish with a StatusSkipped result, records empty output and the skipped
// marker, appends the row, and closes the gate so downstream tasks proceed.
func (st *runState) recordSkip(t *workflow.Task, hooks Hooks) {
	res := TaskResult{TaskID: t.ID, Status: StatusSkipped, Iteration: st.iteration}
	if hooks.OnFinish != nil {
		hooks.OnFinish(*t, st.iteration, res, nil)
	}
	st.mu.Lock()
	st.scope.recordSkipLocked(t.ID)
	st.rep.Tasks = append(st.rep.Tasks, res)
	st.mu.Unlock()
	close(st.gates[t.ID])
}

// recordResult publishes a task that ran to completion: it stamps StatusOK,
// writes the output and succeeded marker, appends the row and adds its usage to
// the report under mu, then closes the gate to release downstream waiters.
func (st *runState) recordResult(t *workflow.Task, res TaskResult) {
	res.Status = StatusOK
	st.mu.Lock()
	st.scope.recordResultLocked(t.ID, res.Output, res.ExitCode)
	st.rep.Tasks = append(st.rep.Tasks, res)
	st.rep.Usage.Add(res.Usage)
	st.mu.Unlock()
	close(st.gates[t.ID])
}

// dispatch substitutes t's body and runs it against the right backend, selected
// by t.BodyKind(): a sub-workflow recurses via Run, a shell task forks `sh -c`,
// a script task execs its file directly (capturing the exit code as data), an
// LLM task calls its runtime (with cache and schema validation). It returns
// the result and any dispatch (runErr) outcome; fatal is a non-nil setup error
// (unknown runtime, unlinked sub-workflow, invalid body) detected before OnStart
// fires and already wrapped with the task id, which the caller returns as-is.
//
// A for_each member binds its loop variable to the current element before the
// normal placeholder substitution, so a {{loopVar}} value containing
// placeholder-looking text is spliced before (not re-expanded across) it.
func dispatch(ctx context.Context, wf *workflow.Workflow, t *workflow.Task, st *runState, hooks Hooks, opts Options, baseDelay time.Duration) (res TaskResult, runErr, fatal error) {
	switch t.BodyKind() {
	case workflow.BodySubWorkflow:
		return dispatchSubWorkflow(ctx, wf, t, st, hooks, opts, baseDelay)
	case workflow.BodyShell:
		return dispatchShell(ctx, wf, t, st, hooks, opts, baseDelay)
	case workflow.BodyScript:
		return dispatchScript(ctx, wf, t, st, hooks, opts, baseDelay)
	case workflow.BodyPrompt:
		return dispatchLLM(ctx, wf, t, st, hooks, opts, baseDelay)
	default:
		// BodyInvalid: the task set none or more than one body form. The parser
		// rejects this, so reaching it means a hand-built or corrupted Task; fail
		// fast rather than silently dispatching down an arbitrary branch.
		return TaskResult{}, nil, fmt.Errorf("task %q: invalid body: exactly one of prompt, command, workflow, or script must be set", t.ID)
	}
}

// dispatchShell is the BodyShell arm of dispatch: it substitutes the command
// line and env, then runs sh -c with retry.
func dispatchShell(ctx context.Context, _ *workflow.Workflow, t *workflow.Task, st *runState, hooks Hooks, opts Options, baseDelay time.Duration) (TaskResult, error, error) {
	if hooks.OnStart != nil {
		hooks.OnStart(*t, st.iteration, "", "", "")
	}
	st.mu.Lock()
	body := workflow.Substitute(bindLoopVar(t.Command, st), st.scope.outputs, opts.Params, opts.State, st.prev, st.scope.exitCodes)
	env := taskEnv(st.scope.outputs, opts.Params, opts.State, st.prev, st.scope.exitCodes, st.loopVar, st.loopVal)
	st.mu.Unlock()
	res, runErr := runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
		return runShell(ctx, t, body, env, st.workDir)
	})
	return res, runErr, nil
}

// dispatchScript is the BodyScript arm of dispatch: it substitutes the script
// path and args, then execs the file directly with retry.
func dispatchScript(ctx context.Context, _ *workflow.Workflow, t *workflow.Task, st *runState, hooks Hooks, opts Options, baseDelay time.Duration) (TaskResult, error, error) {
	if hooks.OnStart != nil {
		hooks.OnStart(*t, st.iteration, "", "", "")
	}
	st.mu.Lock()
	path := workflow.Substitute(bindLoopVar(t.Script, st), st.scope.outputs, opts.Params, opts.State, st.prev, st.scope.exitCodes)
	args := make([]string, len(t.Args))
	for i, a := range t.Args {
		args[i] = workflow.Substitute(bindLoopVar(a, st), st.scope.outputs, opts.Params, opts.State, st.prev, st.scope.exitCodes)
	}
	env := taskEnv(st.scope.outputs, opts.Params, opts.State, st.prev, st.scope.exitCodes, st.loopVar, st.loopVal)
	st.mu.Unlock()
	res, runErr := runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
		return runScript(ctx, t, path, args, env, st.workDir)
	})
	return res, runErr, nil
}

// dispatchLLM is the BodyPrompt arm of dispatch: it looks up the runtime,
// substitutes the prompt, and calls the LLM (with cache and schema validation).
func dispatchLLM(ctx context.Context, wf *workflow.Workflow, t *workflow.Task, st *runState, hooks Hooks, opts Options, baseDelay time.Duration) (TaskResult, error, error) {
	rt, model, effort := wf.EffectiveWithParams(t, opts.Params)
	runner, ok := resolveRunner(opts, rt)
	if !ok {
		return TaskResult{}, nil, fmt.Errorf("task %q: runtime %q: %w", t.ID, rt, runtime.ErrUnknownRuntime)
	}
	sysPrompt := workflow.Substitute(wf.EffectiveSystemPrompt(t), nil, opts.Params, opts.State, nil, nil)
	if hooks.OnStart != nil {
		hooks.OnStart(*t, st.iteration, rt, model, effort)
	}
	st.mu.Lock()
	body := workflow.Substitute(bindLoopVar(t.Prompt, st), st.scope.outputs, opts.Params, opts.State, st.prev, st.scope.exitCodes)
	st.mu.Unlock()
	send := func() (TaskResult, error) {
		return runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
			r, err := runLLM(ctx, t, body, runner, model, effort, sysPrompt, st.workDir)
			if err != nil {
				return r, err
			}
			// A tolerated non-zero exit (ok_exit) means the runtime did not produce
			// a real response, so there is no JSON output to validate against a
			// schema; skip validation and let the empty output flow downstream.
			if r.ExitCode != 0 {
				return r, nil
			}
			return r, validateSchema(t, r.Output)
		})
	}
	var res TaskResult
	var runErr error
	if opts.Cache != nil && wf.CacheEnabled(t) {
		res, runErr = runCached(opts.Cache, t, rt, model, effort, sysPrompt, body, send)
	} else {
		res, runErr = send()
	}
	return res, runErr, nil
}

func resolveRunner(opts Options, name runtime.Name) (runtime.Runner, bool) {
	if opts.Catalog != nil {
		return opts.Catalog.Resolve(name)
	}
	if opts.Resolver != nil {
		return opts.Resolver.Resolve(name)
	}
	return runtime.Default().Resolve(name)
}
