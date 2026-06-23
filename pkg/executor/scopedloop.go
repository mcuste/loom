package executor

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/mcuste/loom/pkg/workflow"
)

// runLoop drives one scoped loop: the synthetic node that stands in for the
// loop's body in the outer schedule. It first waits for the body's external
// (entry) dependencies to resolve, then runs the body sub-DAG iteratively: a
// LoopWhile re-runs until a convergence check passes or its Max cap is hit; a
// LoopForEach runs once per element of its resolved list. Each iteration gets a
// fresh inner gate set; entry-dependency outputs are read once from the outer
// report and pinned for every pass (they are completed tasks, so the shared
// outputs map already holds stable values), while `{{prev.id}}` resolves from
// the previous iteration's body outputs (empty on the first pass).
//
// The final iteration's outputs are already in the shared outputs map, so
// closing each member's outer gate publishes those values to exit consumers
// waiting on them. A for_each over an empty list runs no iterations and falls
// straight through to closing the member gates (members yield empty output).
// Usage accumulates into the shared report across every iteration via runTask.
// Like runTask, a body-task error returns without closing the member gates,
// leaving the errgroup to cancel siblings.
//
// The shared succeeded/skipped maps persist across iterations: runTask records
// succeeded[m]=true (or skipped[m]=true) on every pass and never clears it. A
// body member guarded by `when: not succeeded(m)` therefore runs only on the
// first pass and is skipped on every pass thereafter, silently yielding empty
// output. Status-helper guards over body members are evaluated against this
// monotonically accumulating state, not a per-iteration snapshot.
func runLoop(ctx context.Context, wf *workflow.Workflow, lg *workflow.LoopGroup, st *runState, hooks Hooks, opts Options) error {
	memberSet := make(map[workflow.TaskID]bool, len(lg.Members))
	for _, m := range lg.Members {
		memberSet[m] = true
	}

	// Entry dependencies: the union of member dependencies that point outside
	// the loop, plus a dynamic for_each list-source task. The synthetic node
	// depends on all of them, so block until each has resolved before the first
	// iteration runs (and before the for_each list is resolved).
	entryDeps := make(map[workflow.TaskID]bool)
	for _, m := range lg.Members {
		for _, dep := range wf.ByID(m).DependsOn {
			if !memberSet[dep] {
				entryDeps[dep] = true
			}
		}
	}
	if lg.Kind == workflow.LoopForEach {
		if ref, ok := workflow.ListSourceTaskRef(lg.ListSource); ok && !memberSet[ref] {
			entryDeps[ref] = true
		}
	}
	for dep := range entryDeps {
		select {
		case <-st.gates[dep]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var err error
	if lg.Kind == workflow.LoopForEach {
		err = runForEachLoop(ctx, wf, lg, st, hooks, opts, entryDeps)
	} else {
		err = runWhileLoop(ctx, wf, lg, st, hooks, opts, entryDeps)
	}
	if err != nil {
		return err
	}

	// The final pass's outputs are already in the shared outputs map; closing
	// each member's outer gate releases exit consumers to read those values.
	for _, m := range lg.Members {
		close(st.gates[m])
	}
	return nil
}

// runWhileLoop runs a LoopWhile body until its convergence check passes or the
// Max cap is hit, threading each pass's outputs forward as the next pass's prev.
func runWhileLoop(ctx context.Context, wf *workflow.Workflow, lg *workflow.LoopGroup, st *runState, hooks Hooks, opts Options, entryDeps map[workflow.TaskID]bool) error {
	var prev map[workflow.TaskID]string
	for iter := 1; iter <= lg.Max; iter++ {
		passOutputs, err := runLoopPass(ctx, wf, lg, st, hooks, opts, entryDeps, prev, iter, "", "")
		if err != nil {
			return err
		}
		st.mu.Lock()
		converged, err := loopConverged(lg, st)
		st.mu.Unlock()
		if err != nil {
			return fmt.Errorf("loop %q: convergence check: %w", lg.ID, err)
		}
		if converged {
			break
		}
		prev = passOutputs
	}
	return nil
}

// runForEachLoop resolves the loop's list once (static List, or parseList of the
// substituted ListSource) and runs the body once per element in order, binding
// the loop variable to each element and threading prev forward. An empty list
// runs no passes.
func runForEachLoop(ctx context.Context, wf *workflow.Workflow, lg *workflow.LoopGroup, st *runState, hooks Hooks, opts Options, entryDeps map[workflow.TaskID]bool) error {
	var list []string
	if lg.ListSource == "" {
		list = lg.List
	} else {
		st.mu.Lock()
		resolved := workflow.Substitute(lg.ListSource, st.rep.Outputs, opts.Params, opts.State, nil)
		st.mu.Unlock()
		list = parseList(resolved)
	}
	var prev map[workflow.TaskID]string
	for i := range list {
		passOutputs, err := runLoopPass(ctx, wf, lg, st, hooks, opts, entryDeps, prev, i+1, lg.As, list[i])
		if err != nil {
			return err
		}
		prev = passOutputs
	}
	return nil
}

// runLoopPass runs one pass of a loop body sub-DAG: a fresh gate per member
// (runTask closes the gate it owns, so a member cannot reuse a gate across
// iterations) plus the already-closed outer entry-dependency gates aliased in so
// members satisfy their external waits immediately. Members are dispatched
// concurrently; the pass returns their body outputs, which feed the while
// convergence check and become the next pass's prev. loopVar/loopVal bind a
// for_each iteration variable ("" for a while loop).
func runLoopPass(ctx context.Context, wf *workflow.Workflow, lg *workflow.LoopGroup, st *runState, hooks Hooks, opts Options, entryDeps map[workflow.TaskID]bool, prev map[workflow.TaskID]string, iter int, loopVar, loopVal string) (map[workflow.TaskID]string, error) {
	innerGates := make(map[workflow.TaskID]chan struct{}, len(lg.Members)+len(entryDeps))
	for _, m := range lg.Members {
		innerGates[m] = make(chan struct{})
	}
	for dep := range entryDeps {
		innerGates[dep] = st.gates[dep]
	}

	inner := st.forLoopIteration(innerGates, prev, iter, loopVar, loopVal)
	ig, igctx := errgroup.WithContext(ctx)
	for _, m := range lg.Members {
		t := wf.ByID(m)
		ig.Go(func() error {
			return runTask(igctx, wf, t, inner, hooks, opts)
		})
	}
	if err := ig.Wait(); err != nil {
		return nil, err
	}

	st.mu.Lock()
	passOutputs := make(map[workflow.TaskID]string, len(lg.Members))
	for _, m := range lg.Members {
		passOutputs[m] = st.rep.Outputs[m]
	}
	st.mu.Unlock()
	return passOutputs, nil
}

// loopConverged reports whether the loop's convergence condition holds after a
// pass. For an until_empty loop the target member's trimmed output must be
// empty; for an until loop the compiled condition is evaluated over the current
// member outputs. The caller holds st.mu.
func loopConverged(lg *workflow.LoopGroup, st *runState) (bool, error) {
	if lg.Cond == nil {
		return strings.TrimSpace(st.rep.Outputs[lg.UntilEmpty]) == "", nil
	}
	env := workflow.Env{
		Outputs:   maps.Clone(st.rep.Outputs),
		Succeeded: maps.Clone(st.succeeded),
		Skipped:   maps.Clone(st.skipped),
	}
	return lg.Cond.Eval(env)
}
