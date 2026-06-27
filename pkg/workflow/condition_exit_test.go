package workflow_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestConditionExitRefEval pins that `{{id.exit}}` comparisons read the integer
// exit code from Env.ExitCodes and apply each operator numerically.
func TestConditionExitRefEval(t *testing.T) {
	known := map[workflow.TaskID]bool{"check": true}
	tests := []struct {
		expr string
		code int
		want bool
	}{
		{"{{check.exit}} == 0", 0, true},
		{"{{check.exit}} == 0", 2, false},
		{"{{check.exit}} != 0", 2, true},
		{"{{check.exit}} != 0", 0, false},
		{"{{check.exit}} > 1", 2, true},
		{"{{check.exit}} > 1", 1, false},
		{"{{check.exit}} < 5", 2, true},
		{"{{check.exit}} < 5", 5, false},
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			cond, err := workflow.ParseCondition(tc.expr, known)
			if err != nil {
				t.Fatalf("ParseCondition(%q): %v", tc.expr, err)
			}
			got, err := cond.Eval(workflow.Env{ExitCodes: map[workflow.TaskID]int{"check": tc.code}})
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if got != tc.want {
				t.Errorf("Eval(%q, code=%d) = %v, want %v", tc.expr, tc.code, got, tc.want)
			}
		})
	}
}

// TestConditionExitRefAbsentIsZero pins that an exit ref for a task with no
// recorded code reads 0 (the map zero value).
func TestConditionExitRefAbsentIsZero(t *testing.T) {
	cond, err := workflow.ParseCondition("{{check.exit}} == 0", map[workflow.TaskID]bool{"check": true})
	if err != nil {
		t.Fatalf("ParseCondition: %v", err)
	}
	got, err := cond.Eval(workflow.Env{})
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if !got {
		t.Errorf("Eval with no recorded exit code = false, want true (absent reads 0)")
	}
}

// TestConditionExitRefRejectsQuotedRHS pins that an exit comparison requires a
// bare integer, not a quoted string (the output forms' ==/!= rule does not apply).
func TestConditionExitRefRejectsQuotedRHS(t *testing.T) {
	_, err := workflow.ParseCondition(`{{check.exit}} == "0"`, map[workflow.TaskID]bool{"check": true})
	var malformed *workflow.MalformedConditionError
	if !errors.As(err, &malformed) {
		t.Fatalf("error = %v, want *MalformedConditionError for a quoted exit rhs", err)
	}
}

// TestConditionExitRefUnknownTask pins that the task id before `.exit` is still
// bounded by the known dependency set.
func TestConditionExitRefUnknownTask(t *testing.T) {
	_, err := workflow.ParseCondition("{{ghost.exit}} == 0", map[workflow.TaskID]bool{"check": true})
	var unknown *workflow.UnknownConditionRefError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want *UnknownConditionRefError", err)
	}
}
