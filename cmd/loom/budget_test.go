package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
)

// cmdCostRuntime is a no-binary fake registered for the budget surfacing test.
// Each Run succeeds and reports a fixed cost so a chained workflow accumulates
// a predictable TotalCostUSD and trips the workflow budget.
type cmdCostRuntime struct{}

func (cmdCostRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdCostRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{TotalCostUSD: 0.5},
	}, nil
}

func init() {
	runtime.Register("cmd-cost", cmdCostRuntime{})
}

// TestRunWorkflow_SurfacesBudgetExceeded pins that when the executor aborts on
// the workflow cost budget, runWorkflow surfaces the typed
// executor.BudgetExceededError to the caller rather than swallowing it. The
// three-task chain at cost 0.5 each overruns the 0.75 budget before its last
// task is dispatched.
func TestRunWorkflow_SurfacesBudgetExceeded(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())

	manifest := `name: wf
runtime: cmd-cost
model: m1
budget:
  max_cost_usd: 0.75
tasks:
  - id: a
    prompt: x
  - id: b
    depends_on: [a]
    prompt: x
  - id: c
    depends_on: [b]
    prompt: x
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	var buf bytes.Buffer
	err := runWorkflow(&buf, home, []byte(manifest), wf, resolved, seedPlan{})

	var got *executor.BudgetExceededError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As BudgetExceededError failed; err = %v\noutput:\n%s", err, buf.String())
	}
}
