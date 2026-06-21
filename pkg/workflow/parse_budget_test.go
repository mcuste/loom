package workflow_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParse_TopLevelBudget pins that a well-formed top-level `budget:` block is
// parsed onto the workflow with its positive max_cost_usd.
func TestParse_TopLevelBudget(t *testing.T) {
	t.Parallel()
	src := `
name: wf_budget
runtime: test-rt
model: m1
budget:
  max_cost_usd: 5.0
tasks:
  - id: a
    prompt: A
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.Budget == nil {
		t.Fatalf("wf.Budget = nil, want non-nil")
	}
	if wf.Budget.MaxCostUSD != 5.0 {
		t.Errorf("wf.Budget.MaxCostUSD = %v, want 5.0", wf.Budget.MaxCostUSD)
	}
}

// TestParse_PerTaskBudget pins that a per-task `budget:` block is parsed onto
// the task with its positive max_cost_usd.
func TestParse_PerTaskBudget(t *testing.T) {
	t.Parallel()
	src := `
name: wf_budget
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
    budget:
      max_cost_usd: 2.5
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	task := wf.ByID("a")
	if task == nil {
		t.Fatalf("task a not found")
	}
	if task.Budget == nil {
		t.Fatalf("task.Budget = nil, want non-nil")
	}
	if task.Budget.MaxCostUSD != 2.5 {
		t.Errorf("task.Budget.MaxCostUSD = %v, want 2.5", task.Budget.MaxCostUSD)
	}
}

// TestParse_NoBudgetIsNil pins that a workflow with no `budget:` key parses to
// a nil Budget at both the workflow and task level (the no-limit default).
func TestParse_NoBudgetIsNil(t *testing.T) {
	t.Parallel()
	src := `
name: wf_nobudget
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.Budget != nil {
		t.Errorf("wf.Budget = %+v, want nil", wf.Budget)
	}
	if got := wf.ByID("a"); got == nil || got.Budget != nil {
		t.Errorf("task.Budget = %+v, want nil", got.Budget)
	}
}

// TestParse_RejectsNonPositiveBudget pins that a top-level max_cost_usd that is
// not a positive float (zero or negative) is rejected with InvalidBudgetError.
func TestParse_RejectsNonPositiveBudget(t *testing.T) {
	t.Parallel()
	for _, val := range []string{"0", "-1", "-0.5"} {
		t.Run(val, func(t *testing.T) {
			t.Parallel()
			src := `
name: wf_budget
runtime: test-rt
model: m1
budget:
  max_cost_usd: ` + val + `
tasks:
  - id: a
    prompt: A
`
			_, err := workflow.Parse([]byte(src))
			var got *workflow.InvalidBudgetError
			if !errors.As(err, &got) {
				t.Fatalf("max_cost_usd=%s: errors.As InvalidBudgetError failed; err = %v", val, err)
			}
		})
	}
}

// TestParse_RejectsEmptyBudgetMapping pins the distinct failure mode where the
// `budget:` block is present but the `max_cost_usd` key is absent: the
// zero-value path yields InvalidBudgetError{Value: 0}, not a malformed-structure
// error.
func TestParse_RejectsEmptyBudgetMapping(t *testing.T) {
	t.Parallel()
	src := `
name: wf_budget
runtime: test-rt
model: m1
budget: {}
tasks:
  - id: a
    prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.InvalidBudgetError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As InvalidBudgetError failed; err = %v", err)
	}
	if got.Value != 0 {
		t.Errorf("InvalidBudgetError.Value = %v, want 0", got.Value)
	}
}

// TestParse_RejectsNonPositivePerTaskBudget pins that a per-task max_cost_usd
// that is not a positive float is rejected with InvalidBudgetError.
func TestParse_RejectsNonPositivePerTaskBudget(t *testing.T) {
	t.Parallel()
	src := `
name: wf_budget
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
    budget:
      max_cost_usd: 0
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.InvalidBudgetError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As InvalidBudgetError failed; err = %v", err)
	}
}
