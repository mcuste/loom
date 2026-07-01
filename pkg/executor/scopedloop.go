package executor

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/mcuste/loom/pkg/workflow"
)

type loopRunner struct {
	ctx    context.Context
	interp *interpreter
	lp     *loopProgram
	st     *frame
}

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
// Usage accumulates into the shared report across every iteration via
// interpreter.evalNode. Like top-level task evaluation, a body-task error
// returns without closing the member gates, leaving the errgroup to cancel
// siblings.
//
// The shared succeeded/skipped maps persist across iterations:
// interpreter.evalNode records succeeded[m]=true (or skipped[m]=true) on every
// pass and never clears it. A body member guarded by `when: not succeeded(m)`
// therefore runs only on the first pass and is skipped on every pass
// thereafter, silently yielding empty output. Status-helper guards over body
// members are evaluated against this monotonically accumulating state, not a
// per-iteration snapshot.
func runLoop(ctx context.Context, i *interpreter, lp *loopProgram, st *frame) error {
	lg := lp.group

	// Entry dependencies are compiled once from member dependencies plus any
	// external dynamic for_each list source. The synthetic loop node depends on
	// all of them, so block until each has resolved before the first iteration
	// runs and before a dynamic list is resolved.
	for dep := range lp.entryDeps {
		select {
		case <-st.gates[dep]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	lr := loopRunner{
		ctx:    ctx,
		interp: i,
		lp:     lp,
		st:     st,
	}

	var err error
	switch {
	case lg.Kind == workflow.LoopForEach && lg.Parallel:
		err = lr.runForEachParallel()
	case lg.Kind == workflow.LoopForEach:
		err = lr.runForEachLoop()
	default:
		err = lr.runWhileLoop()
	}
	if err != nil {
		return err
	}

	// The final pass's outputs are already in the shared outputs map; closing
	// each member's outer gate releases exit consumers to read those values.
	for _, m := range lp.members {
		close(st.gates[m])
	}
	return nil
}

// runWhileLoop runs a LoopWhile body until its convergence check passes or the
// Max cap is hit, threading each pass's outputs forward as the next pass's prev.
func (lr loopRunner) runWhileLoop() error {
	var prev map[workflow.TaskID]string
	for iter := 1; iter <= lr.lp.group.Max; iter++ {
		passOutputs, err := lr.runLoopPass(prev, iter, "", "")
		if err != nil {
			return err
		}
		lr.st.mu.Lock()
		converged, err := lr.loopConverged()
		lr.st.mu.Unlock()
		if err != nil {
			return fmt.Errorf("loop %q: convergence check: %w", lr.lp.group.ID, err)
		}
		if converged {
			break
		}
		prev = passOutputs
	}
	return nil
}

// forEachList resolves a for_each loop's element list: the static List as-is, or
// parseList of the substituted ListSource for a dynamic source.
func (lr loopRunner) forEachList() []string {
	if lr.lp.group.ListSource == "" {
		return lr.lp.group.List
	}
	lr.st.mu.Lock()
	resolved := workflow.Substitute(lr.lp.group.ListSource, lr.st.scope.outputs, lr.interp.opts.Params, lr.interp.opts.State, nil, lr.st.scope.exitCodes)
	lr.st.mu.Unlock()
	return parseList(resolved)
}

// runForEachLoop resolves the loop's list once and runs the body once per
// element in order, binding the loop variable to each element and threading prev
// forward. An empty list runs no passes.
func (lr loopRunner) runForEachLoop() error {
	list := lr.forEachList()
	var prev map[workflow.TaskID]string
	for i := range list {
		passOutputs, err := lr.runLoopPass(prev, i+1, lr.lp.group.As, list[i])
		if err != nil {
			return err
		}
		prev = passOutputs
	}
	return nil
}

// runForEachParallel resolves the loop's list once and runs the body for every
// element concurrently, binding the loop variable per element. Each pass runs
// over a private snapshot of the member outputs (see forParallelIteration), so
// passes never observe or clobber one another; after a pass completes its member
// outputs are merged back into the shared report. The first pass to error
// cancels the rest via the errgroup context. An empty list runs no passes.
func (lr loopRunner) runForEachParallel() error {
	list := lr.forEachList()
	pg, pgctx := errgroup.WithContext(lr.ctx)
	for i := range list {
		iter, val := i+1, list[i]
		pg.Go(func() error {
			return lr.runParallelPass(pgctx, iter, lr.lp.group.As, val)
		})
	}
	return pg.Wait()
}

// buildInnerGates allocates a fresh gate channel for each loop member and
// aliases the already-closed outer gates for each entry dependency into the
// same map, so inner runState instances can satisfy both member and external
// waits without any additional coordination.
func (lr loopRunner) buildInnerGates() map[workflow.TaskID]chan struct{} {
	innerGates := make(map[workflow.TaskID]chan struct{}, len(lr.lp.members)+len(lr.lp.entryDeps))
	for _, m := range lr.lp.members {
		innerGates[m] = make(chan struct{})
	}
	for dep := range lr.lp.entryDeps {
		innerGates[dep] = lr.st.gates[dep]
	}
	return innerGates
}

// runMembers dispatches all loop members concurrently over inner via an
// errgroup and waits for them to complete. The first member error cancels the
// rest via the errgroup context.
func (lr loopRunner) runMembers(ctx context.Context, inner *runState) error {
	ig, igctx := errgroup.WithContext(ctx)
	for _, m := range lr.lp.members {
		id := m
		ig.Go(func() error {
			n := lr.interp.program.nodes[id]
			if n == nil || n.task == nil {
				return fmt.Errorf("task %q: compiled node missing", id)
			}
			return lr.interp.evalNode(igctx, inner, n)
		})
	}
	return ig.Wait()
}

// runParallelPass runs one pass of a parallel for_each body: a fresh gate per
// member plus the aliased entry-dependency gates, over an isolated runState.
// After the members complete it merges their outputs and succeeded/skipped
// dispositions back into the shared report under st.mu so exit consumers (and
// outer status guards) observe a member value.
//
// The merge makes a member's disposition coherent under the race: a pass that
// ran the member to completion wins over one that skipped it (its real output is
// published and any prior skip mark is cleared), and a skipped pass neither
// clobbers an already-published real output nor downgrades a success. A member
// is therefore reported succeeded if ANY element ran it (with that element's
// output) and skipped only if EVERY element skipped it; never both. Which
// succeeding element's value survives is still unspecified, since the passes
// race; a downstream task that needs a specific element must reference that
// element, not the loop member.
func (lr loopRunner) runParallelPass(ctx context.Context, iter int, loopVar, loopVal string) error {
	innerGates := lr.buildInnerGates()
	inner := lr.st.forParallelIteration(innerGates, iter, loopVar, loopVal)
	if err := lr.runMembers(ctx, inner); err != nil {
		return err
	}

	lr.st.mu.Lock()
	lr.st.scope.mergeParallelLocked(lr.lp.members, inner.scope)
	lr.st.mu.Unlock()
	return nil
}

// runLoopPass runs one pass of a loop body sub-DAG: a fresh gate per member
// (interpreter.evalNode closes the gate it owns, so a member cannot reuse a
// gate across iterations) plus the already-closed outer entry-dependency gates
// aliased in so members satisfy their external waits immediately. Members are
// dispatched concurrently; the pass returns their body outputs, which feed the
// while convergence check and become the next pass's prev. loopVar/loopVal
// bind a for_each iteration variable ("" for a while loop).
func (lr loopRunner) runLoopPass(prev map[workflow.TaskID]string, iter int, loopVar, loopVal string) (map[workflow.TaskID]string, error) {
	innerGates := lr.buildInnerGates()
	inner := lr.st.forLoopIteration(innerGates, prev, iter, loopVar, loopVal)
	if err := lr.runMembers(lr.ctx, inner); err != nil {
		return nil, err
	}

	lr.st.mu.Lock()
	passOutputs := lr.st.scope.passOutputsLocked(lr.lp.members)
	lr.st.mu.Unlock()
	return passOutputs, nil
}

// loopConverged reports whether the loop's convergence condition holds after a
// pass. For an until_empty loop the target member's trimmed output must be
// empty; for an until loop the compiled condition is evaluated over the current
// member outputs. The caller holds st.mu.
func (lr loopRunner) loopConverged() (bool, error) {
	if lr.lp.group.Cond == nil {
		return strings.TrimSpace(lr.st.scope.outputs[lr.lp.group.UntilEmpty]) == "", nil
	}
	env := lr.st.scope.toEnvLocked()
	return lr.lp.group.Cond.Eval(env)
}
