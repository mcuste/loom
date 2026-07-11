package executor

import (
	"context"
	"strings"
	"sync"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// bindLoopVar replaces a for_each member's `{{loopVar}}` placeholder with the
// current iteration's element before the normal placeholder substitution runs.
// It is a no-op outside a for_each loop (st.loopVar == ""), where the binding
// is empty and the body passes through unchanged.
func bindLoopVar(body string, st *frame) string {
	if st.loopVar == "" {
		return body
	}
	return strings.ReplaceAll(body, "{{"+st.loopVar+"}}", st.loopVal)
}

// sharedFrame holds the report/store pair shared by every frame in one Run
// call.
// Every field is read or written under mu, except workDir (immutable after
// init).
type sharedFrame struct {
	rep *Report
	// scope is the current frame store: the four run-scope maps (outputs,
	// succeeded, skipped, exitCodes) that are always cloned and merged together.
	// outputs aliases
	// rep.Outputs for the top-level schedule, sequential loops, and while loops,
	// so those paths publish straight into the report. A parallel for_each
	// iteration swaps in a private copy (seeded from a snapshot of the shared
	// scope) so concurrent passes neither observe nor clobber one another's
	// member results; the driver merges each pass's scope back afterward.
	scope store
	mu    *sync.Mutex
	// budget is a pointer so a scoped-loop iteration's derived frame (fresh
	// gates, but the same report, mutex, and budget slot) serializes
	// budget-gated dispatch against the outer schedule rather than against a
	// private copy of the gate.
	budget *budgetGate
	// workDir is the cwd every task process in this run is launched in (the LLM
	// runtime, shell, and script alike), resolved once in Run from the workflow's
	// working_dir. "" inherits loom's process cwd. Immutable after init.
	workDir string
}

// loopFrame is the per-pass context carried by value to each goroutine.
// None of its fields require mu: they are either immutable per-pass (iteration,
// loopVar, loopVal, prev) or concurrency-safe by design (gates: channels, closed
// once, never re-opened).
type loopFrame struct {
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

// frame is the current interpreter frame. It combines the shared run state
// with one frame's loop/pass context so executor helpers get direct field
// access (st.mu, st.scope, st.gates, st.iteration) without changing stable
// call sites.
type frame struct {
	*sharedFrame
	loopFrame
}

// childForLoopPass derives a sequential loop-pass frame. The child frame
// reuses the parent's report, store, mutex, and budget gate, but swaps in
// pass-local gates and loop bindings.
func (st *frame) childForLoopPass(gates map[workflow.TaskID]chan struct{}, prev map[workflow.TaskID]string, iteration int, loopVar, loopVal string) *frame {
	return &frame{
		sharedFrame: st.sharedFrame,
		loopFrame: loopFrame{
			gates:     gates,
			prev:      prev,
			iteration: iteration,
			loopVar:   loopVar,
			loopVal:   loopVal,
		},
	}
}

// childForParallelPass derives a parallel for_each pass frame. The child frame
// snapshots the store so sibling passes do not observe or clobber one
// another's member state, while still sharing the parent report, mutex, and
// budget gate.
func (st *frame) childForParallelPass(gates map[workflow.TaskID]chan struct{}, iteration int, loopVar, loopVal string) *frame {
	sh := &sharedFrame{
		rep:     st.rep,
		scope:   st.scope.clone(st.mu),
		mu:      st.mu,
		budget:  st.budget,
		workDir: st.workDir,
	}
	return &frame{
		sharedFrame: sh,
		loopFrame: loopFrame{
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
func (st *frame) waitDeps(ctx context.Context, deps []workflow.TaskID) error {
	for _, dep := range deps {
		select {
		case <-st.gates[dep]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// evalWhen evaluates a compiled `when:` guard against a snapshot of the current
// outputs/succeeded/skipped state and reports whether the task should run. The
// caller guarantees cond != nil; Cond was compiled at load time.
func (st *frame) evalWhen(_ workflow.TaskID, cond *workflow.Condition) (bool, error) {
	env := st.scope.envSnapshot(st.mu)
	return cond.Eval(env)
}

// admitBudget delegates to the run's [budgetGate], passing a closure that
// reads the cumulative cost under the gate's lock. See [budgetGate.admit] for
// the full serialization protocol.
func (st *frame) admitBudget(ctx context.Context, budget *workflow.Budget) (func(), error) {
	if budget == nil {
		return func() {}, nil
	}
	return st.budget.admit(ctx, func() float64 {
		return st.rep.Usage.TotalCostUSD
	}, budget.MaxCostUSD)
}

// recordSkip publishes a `when:`-guarded task that was skipped: it fires
// OnFinish with a StatusSkipped result, records empty output and the skipped
// marker, appends the row, and closes the gate so downstream tasks proceed.
func (st *frame) recordSkip(t *workflow.Task, hooks Hooks) {
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
func (st *frame) recordResult(t *workflow.Task, res TaskResult) {
	res.Status = StatusOK
	st.mu.Lock()
	st.scope.recordResultLocked(t.ID, res.Output, res.ExitCode)
	st.rep.Tasks = append(st.rep.Tasks, res)
	st.rep.Usage.Add(res.Usage)
	st.mu.Unlock()
	close(st.gates[t.ID])
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
