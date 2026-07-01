package executor

import (
	"context"
	"sync"
)

// budgetGate serializes budget-gated dispatches so the check-then-commit
// is atomic. A workflow budget is enforced against the cumulative cost of
// completed tasks, but a task's cost is only known after it runs; without
// serialization, two goroutines whose deps resolve concurrently both read a
// stale spend, both pass the check, and the concurrent subgraph overshoots
// the limit. Admitting at most one budget-gated task at a time guarantees its
// cost is recorded before the next task's check runs, bounding the overshoot
// to the single task that crosses the limit (matching the serial semantics).
type budgetGate struct {
	inFlight bool
	// ready wakes a waiter once the in-flight task has recorded its cost and
	// released the slot. Its L is the run-global mutex (sharedFrame.mu).
	ready *sync.Cond
}

// admit blocks until no other budget-gated task is in flight, then claims the
// slot. It acquires and releases g.ready.L itself; the caller must NOT hold
// it. getSpent is called once under the lock to read the current cumulative
// cost, so the check-then-commit is atomic with respect to the mutex.
//
// Returns the release func the caller must defer; on a cost overshoot or a
// context cancellation it returns a no-op release and an error.
func (g *budgetGate) admit(ctx context.Context, getSpent func() float64, limit float64) (func(), error) {
	noop := func() {}
	g.ready.L.Lock()
	for g.inFlight {
		g.ready.Wait()
		// A wake may come from a sibling's cancellation rather than a slot
		// release; bail without claiming the slot so we never block g.Wait.
		if ctx.Err() != nil {
			g.ready.L.Unlock()
			return noop, ctx.Err()
		}
	}
	spent := getSpent()
	if spent > limit {
		// Wake peers so they re-evaluate and drain (each will also abort)
		// rather than block forever on the slot this goroutine never takes.
		g.ready.Broadcast()
		g.ready.L.Unlock()
		return noop, &BudgetExceededError{Limit: limit, Spent: spent}
	}
	g.inFlight = true
	g.ready.L.Unlock()
	return func() {
		g.ready.L.Lock()
		g.inFlight = false
		g.ready.Broadcast()
		g.ready.L.Unlock()
	}, nil
}
