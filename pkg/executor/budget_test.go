package executor_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// costRuntime is a registered fake whose every Run succeeds, echoing the prompt
// and reporting a fixed per-call cost. Budget tests chain such tasks so the
// cumulative TotalCostUSD grows by a known step per completed task.
type costRuntime struct{ cost float64 }

func (costRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (c costRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{TotalCostUSD: c.cost},
	}, nil
}

// costFlakyRuntime always fails with failErr and reports a fixed cost per call.
// It counts calls so a per-task-budget test can assert how many retry attempts
// the executor made before the budget capped them.
type costFlakyRuntime struct {
	mu      sync.Mutex
	calls   int
	cost    float64
	failErr error
}

func (f *costFlakyRuntime) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *costFlakyRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (f *costFlakyRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return runtime.Response{Usage: runtime.Usage{TotalCostUSD: f.cost}}, f.failErr
}

// budgetSeq keeps each costFlakyRuntime registration name unique across
// -count=N and parallel runs, mirroring flakySeq in retry_test.go.
var budgetSeq atomic.Uint64

func init() {
	runtime.Register("budget-cost", costRuntime{cost: 0.5})
}

// chain builds a linear workflow id_0 -> id_1 -> ... of n tasks against the
// budget-cost runtime (cost 0.5 each), with the given workflow-level budget.
// The chain forces serial execution so cumulative cost grows one step at a
// time and the pre-dispatch budget check is deterministic.
func chain(n int, budget *workflow.Budget) *workflow.Workflow {
	tasks := make([]workflow.Task, n)
	for i := range n {
		id := workflow.TaskID("t" + strconv.Itoa(i))
		tasks[i] = workflow.Task{ID: id, Prompt: "x"}
		if i > 0 {
			tasks[i].DependsOn = []workflow.TaskID{workflow.TaskID("t" + strconv.Itoa(i-1))}
		}
	}
	return &workflow.Workflow{
		ID:      "wf",
		Runtime: "budget-cost",
		Model:   "m1",
		Budget:  budget,
		Tasks:   tasks,
	}
}

// TestRun_UnderBudgetCompletes pins that a workflow whose total cost stays
// below the budget runs every task and returns no error.
func TestRun_UnderBudgetCompletes(t *testing.T) {
	t.Parallel()
	wf := chain(3, &workflow.Budget{MaxCostUSD: 2.0}) // total cost 1.5 < 2.0

	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(rep.Tasks) != 3 {
		t.Errorf("rep.Tasks = %d, want 3 (all tasks ran)", len(rep.Tasks))
	}
}

// TestRun_ExactBudgetCompletes pins the inclusive boundary: a workflow whose
// total cost lands exactly on the budget still completes without error. The
// budget is "exceeded" only by spend strictly greater than the limit.
func TestRun_ExactBudgetCompletes(t *testing.T) {
	t.Parallel()
	wf := chain(2, &workflow.Budget{MaxCostUSD: 1.0}) // total cost exactly 1.0

	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run returned error at exact budget: %v", err)
	}
	if len(rep.Tasks) != 2 {
		t.Errorf("rep.Tasks = %d, want 2 (all tasks ran)", len(rep.Tasks))
	}
}

// TestRun_OverBudgetAbortsBeforeDispatch pins that once the cumulative cost of
// completed tasks exceeds the budget, the executor aborts BEFORE dispatching
// the next task and returns a BudgetExceededError carrying the limit and the
// spend so far. With a 3-task chain at cost 0.5 and a 0.75 budget, tasks t0 and
// t1 complete (spend 1.0 > 0.75) and t2 is never dispatched.
func TestRun_OverBudgetAbortsBeforeDispatch(t *testing.T) {
	t.Parallel()
	wf := chain(3, &workflow.Budget{MaxCostUSD: 0.75})

	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})

	var got *executor.BudgetExceededError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As BudgetExceededError failed; err = %v", err)
	}
	if got.Limit != 0.75 {
		t.Errorf("BudgetExceededError.Limit = %v, want 0.75", got.Limit)
	}
	if got.Spent != 1.0 {
		t.Errorf("BudgetExceededError.Spent = %v, want 1.0", got.Spent)
	}
	if rep == nil || len(rep.Tasks) != 2 {
		t.Errorf("rep.Tasks = %d, want 2 (t2 never dispatched)", len(rep.Tasks))
	}
}

// TestRun_PerTaskBudgetCapsRetries pins that a per-task budget caps that task's
// retries: a runtime that always fails transiently at cost 0.5 per attempt,
// under retry.max=10 but a 1.0 per-task budget, is invoked only until its
// accumulated cost would exceed the budget — three attempts (0.5, 1.0, 1.5),
// not the eleven that retry.max alone would permit.
func TestRun_PerTaskBudgetCapsRetries(t *testing.T) {
	t.Parallel()
	flaky := &costFlakyRuntime{cost: 0.5, failErr: errors.New(transientMsg)}
	name := "budget-flaky-" + t.Name() + "-" + strconv.FormatUint(budgetSeq.Add(1), 10)
	runtime.Register(runtime.Name(name), flaky)

	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: runtime.Name(name),
		Model:   "m1",
		Tasks: []workflow.Task{{
			ID:     "a",
			Prompt: "x",
			Retry:  workflow.Retry{Max: 10, Backoff: workflow.BackoffNone, On: []string{"transient"}},
			Budget: &workflow.Budget{MaxCostUSD: 1.0},
		}},
	}

	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{RetryBaseDelay: fastBackoff})
	var got *executor.BudgetExceededError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As BudgetExceededError failed; err = %v", err)
	}
	if got.Limit != 1.0 {
		t.Errorf("BudgetExceededError.Limit = %v, want 1.0", got.Limit)
	}
	if got.Spent != 1.5 {
		t.Errorf("BudgetExceededError.Spent = %v, want 1.5", got.Spent)
	}
	if n := flaky.callCount(); n != 3 {
		t.Errorf("attempts = %d, want 3 (per-task budget caps retries)", n)
	}
}
