package executor

import (
	"context"
	"fmt"
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
func (i *interpreter) runLoop(ctx context.Context, st *frame, lp *loopProgram) error {
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

	var err error
	switch {
	case lg.Kind == workflow.LoopForEach && lg.Parallel:
		err = i.runForEachParallel(ctx, st, lp)
	case lg.Kind == workflow.LoopForEach:
		err = i.runForEachLoop(ctx, st, lp)
	default:
		err = i.runWhileLoop(ctx, st, lp)
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
func (i *interpreter) runWhileLoop(ctx context.Context, st *frame, lp *loopProgram) error {
	var prev map[workflow.TaskID]string
	for iter := 1; iter <= lp.group.Max; iter++ {
		passOutputs, err := i.runLoopPass(ctx, st, lp, prev, iter, "", "")
		if err != nil {
			return err
		}
		st.mu.Lock()
		converged, err := i.loopConverged(st, lp)
		st.mu.Unlock()
		if err != nil {
			return fmt.Errorf("loop %q: convergence check: %w", lp.group.ID, err)
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
func (i *interpreter) forEachList(st *frame, lp *loopProgram) []string {
	if lp.group.ListSource == "" {
		return lp.group.List
	}
	st.mu.Lock()
	resolved := workflow.Substitute(lp.group.ListSource, st.scope.outputs, i.opts.Params, i.opts.State, nil, st.scope.exitCodes)
	st.mu.Unlock()
	return parseList(resolved)
}

// runForEachLoop resolves the loop's list once and runs the body once per
// element in order, binding the loop variable to each element and threading prev
// forward. An empty list runs no passes.
func (i *interpreter) runForEachLoop(ctx context.Context, st *frame, lp *loopProgram) error {
	list := i.forEachList(st, lp)
	var prev map[workflow.TaskID]string
	for idx := range list {
		passOutputs, err := i.runLoopPass(ctx, st, lp, prev, idx+1, lp.group.As, list[idx])
		if err != nil {
			return err
		}
		prev = passOutputs
	}
	return nil
}

// runForEachParallel resolves the loop's list once and runs the body for every
// element concurrently, binding the loop variable per element. Each pass runs
// in a child frame with a private snapshot of the member store (see
// childFrameForParallelLoopPass), so passes never observe or clobber one
// another; after a pass completes its member outputs are merged back into the
// shared report. The first pass to error cancels the rest via the errgroup
// context. An empty list runs no passes.
func (i *interpreter) runForEachParallel(ctx context.Context, st *frame, lp *loopProgram) error {
	list := i.forEachList(st, lp)
	pg, pgctx := errgroup.WithContext(ctx)
	for idx := range list {
		iter, val := idx+1, list[idx]
		pg.Go(func() error {
			return i.runParallelPass(pgctx, st, lp, iter, lp.group.As, val)
		})
	}
	return pg.Wait()
}

// runMembers dispatches all loop members concurrently over inner via an
// errgroup and waits for them to complete. The first member error cancels the
// rest via the errgroup context.
func (i *interpreter) runLoopMembers(ctx context.Context, st *frame, lp *loopProgram) error {
	ig, igctx := errgroup.WithContext(ctx)
	for _, id := range lp.members {
		id := id
		ig.Go(func() error {
			return i.evalNode(igctx, st, i.program.nodes[id])
		})
	}
	return ig.Wait()
}

// runParallelPass runs one pass of a parallel for_each body: a fresh gate per
// member plus the aliased entry-dependency gates, over an isolated child frame.
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
func (i *interpreter) runParallelPass(ctx context.Context, st *frame, lp *loopProgram, iter int, loopVar, loopVal string) error {
	innerGates := lp.buildInnerGates(st.gates)
	inner := st.childFrameForParallelLoopPass(innerGates, iter, loopVar, loopVal)
	if err := i.runLoopMembers(ctx, inner, lp); err != nil {
		return err
	}

	st.mu.Lock()
	st.scope.mergeParallelLocked(lp.members, inner.scope)
	st.mu.Unlock()
	return nil
}

// runLoopPass runs one pass of a loop body sub-DAG: a fresh gate per member
// (interpreter.evalNode closes the gate it owns, so a member cannot reuse a
// gate across iterations) plus the already-closed outer entry-dependency gates
// aliased in so members satisfy their external waits immediately. Members are
// dispatched concurrently in a child frame that shares the outer report and
// store; the pass returns their body outputs, which feed the while convergence
// check and become the next pass's prev. loopVar/loopVal bind a for_each
// iteration variable ("" for a while loop).
func (i *interpreter) runLoopPass(ctx context.Context, st *frame, lp *loopProgram, prev map[workflow.TaskID]string, iter int, loopVar, loopVal string) (map[workflow.TaskID]string, error) {
	innerGates := lp.buildInnerGates(st.gates)
	inner := st.childFrameForLoopPass(innerGates, prev, iter, loopVar, loopVal)
	if err := i.runLoopMembers(ctx, inner, lp); err != nil {
		return nil, err
	}

	st.mu.Lock()
	passOutputs := st.scope.passOutputsLocked(lp.members)
	st.mu.Unlock()
	return passOutputs, nil
}

// loopConverged reports whether the loop's convergence condition holds after a
// pass. For an until_empty loop the target member's trimmed output must be
// empty; for an until loop the compiled condition is evaluated over the current
// member outputs. The caller holds st.mu.
func (i *interpreter) loopConverged(st *frame, lp *loopProgram) (bool, error) {
	if lp.group.Cond == nil {
		return strings.TrimSpace(st.scope.outputs[lp.group.UntilEmpty]) == "", nil
	}
	env := st.scope.toEnvLocked()
	return lp.group.Cond.Eval(env)
}
