package executor

import (
	"context"
	"fmt"

	"github.com/mcuste/loom/pkg/workflow"
)

// Gate is a policy extension point evaluated by the interpreter at well-known
// run or step boundaries. Built-in gates cover workflow budgets and task
// `when:` conditions; future approval or policy checks can implement the same
// contract without changing task execution.
type Gate interface {
	Evaluate(context.Context, GateContext) GateDecision
}

// GateContext carries the immutable workflow/task facts for a gate decision.
// The interpreter attaches its private frame so built-in gates can read run
// state without exposing executor internals across package boundaries.
type GateContext struct {
	Task      *workflow.Task
	Condition *workflow.Condition
	Iteration int
	Output    string
	ExitCode  int
	Schema    *workflow.Schema
	Budget    *workflow.Budget

	state *frame
}

// GateDecision is the outcome of one gate evaluation.
type GateDecision struct {
	Allowed bool
	Skipped bool
	Reason  string
	Err     error
	Release func()
}

type whenConditionGate struct{}

type budgetPolicyGate struct{}

type schemaGate struct{}

func (whenConditionGate) Evaluate(_ context.Context, gc GateContext) GateDecision {
	run, err := gc.state.evalWhen(gc.Task.ID, gc.Condition)
	if err != nil {
		return GateDecision{Err: fmt.Errorf("task %q: when: %w", gc.Task.ID, err)}
	}
	if !run {
		return GateDecision{Allowed: false, Skipped: true, Reason: "when condition is false"}
	}
	return GateDecision{Allowed: true}
}

func (budgetPolicyGate) Evaluate(ctx context.Context, gc GateContext) GateDecision {
	release, err := gc.state.admitBudget(ctx, gc.Budget)
	if err != nil {
		return GateDecision{Err: err}
	}
	return GateDecision{Allowed: true, Release: release}
}

func (schemaGate) Evaluate(_ context.Context, gc GateContext) GateDecision {
	if gc.Schema == nil || gc.ExitCode != 0 {
		return GateDecision{Allowed: true}
	}
	if err := validateSchema(gc.Task.ID, gc.Schema, gc.Output); err != nil {
		return GateDecision{Err: err}
	}
	return GateDecision{Allowed: true}
}

func (i *interpreter) preStepGates(n *node) []Gate {
	gates := make([]Gate, 0, 2)
	if n.when() != nil {
		gates = append(gates, whenConditionGate{})
	}
	if i.program.env.budget != nil {
		gates = append(gates, budgetPolicyGate{})
	}
	return gates
}

func evaluateSchemaGate(ctx context.Context, t *workflow.Task, schema *workflow.Schema, output string, exitCode int) error {
	decision := schemaGate{}.Evaluate(ctx, GateContext{Task: t, Schema: schema, Output: output, ExitCode: exitCode})
	return decision.Err
}

func (i *interpreter) evaluatePreStepGates(ctx context.Context, st *frame, n *node) (release func(), skipped bool, err error) {
	var releases []func()
	cleanup := func() {
		for j := len(releases) - 1; j >= 0; j-- {
			releases[j]()
		}
	}
	for _, gate := range i.preStepGates(n) {
		decision := gate.Evaluate(ctx, GateContext{
			Task:      n.taskPayload(),
			Condition: n.when(),
			Iteration: st.iteration,
			Budget:    i.program.env.budget,
			state:     st,
		})
		if decision.Err != nil {
			cleanup()
			return nil, false, decision.Err
		}
		if decision.Release != nil {
			releases = append(releases, decision.Release)
		}
		if decision.Skipped {
			cleanup()
			return nil, true, nil
		}
	}
	return cleanup, false, nil
}
