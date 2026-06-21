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
// (entry) dependencies to resolve, then runs the body sub-DAG iteratively until
// a convergence check passes or the loop's Max cap is hit. Each iteration gets a
// fresh inner gate set; entry-dependency outputs are read once from the outer
// report and pinned for every pass (they are completed tasks, so the shared
// outputs map already holds stable values), while `{{prev.id}}` resolves from
// the previous iteration's body outputs (empty on the first pass).
//
// On convergence the final iteration's outputs are already in the shared
// outputs map, so closing each member's outer gate publishes those values to
// exit consumers waiting on them. Usage accumulates into the shared report
// across every iteration via runTask. Like runTask, a body-task error returns
// without closing the member gates, leaving the errgroup to cancel siblings.
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
	// the loop. The synthetic node depends on all of them, so block until each
	// has resolved before the first iteration runs.
	entryDeps := make(map[workflow.TaskID]bool)
	for _, m := range lg.Members {
		for _, dep := range wf.ByID(m).DependsOn {
			if !memberSet[dep] {
				entryDeps[dep] = true
			}
		}
	}
	for dep := range entryDeps {
		select {
		case <-st.gates[dep]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// prev holds the previous iteration's body outputs; nil on the first pass so
	// `{{prev.id}}` collapses to empty.
	var prev map[workflow.TaskID]string
	for iter := 1; iter <= lg.Max; iter++ {
		// A fresh gate per member each pass: runTask closes the gate it owns, so a
		// member cannot reuse a gate across iterations. Entry-dependency gates are
		// the already-closed outer gates, letting a member's runTask satisfy its
		// external waits immediately.
		innerGates := make(map[workflow.TaskID]chan struct{}, len(lg.Members)+len(entryDeps))
		for _, m := range lg.Members {
			innerGates[m] = make(chan struct{})
		}
		for dep := range entryDeps {
			innerGates[dep] = st.gates[dep]
		}

		inner := st.forLoopIteration(innerGates, prev, iter)
		ig, igctx := errgroup.WithContext(ctx)
		for _, m := range lg.Members {
			t := wf.ByID(m)
			ig.Go(func() error {
				return runTask(igctx, wf, t, inner, hooks, opts)
			})
		}
		if err := ig.Wait(); err != nil {
			return err
		}

		// Snapshot this pass's body outputs: they feed the convergence check and
		// become the next iteration's prev.
		st.mu.Lock()
		passOutputs := make(map[workflow.TaskID]string, len(lg.Members))
		for _, m := range lg.Members {
			passOutputs[m] = st.rep.Outputs[m]
		}
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

	// The final pass's outputs are already in the shared outputs map; closing
	// each member's outer gate releases exit consumers to read those values.
	for _, m := range lg.Members {
		close(st.gates[m])
	}
	return nil
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
