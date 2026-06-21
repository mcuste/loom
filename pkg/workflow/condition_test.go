package workflow_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// known builds the referenceable-id set ParseCondition takes, from a list of
// task id strings.
func known(ids ...string) map[workflow.TaskID]bool {
	m := make(map[workflow.TaskID]bool, len(ids))
	for _, id := range ids {
		m[workflow.TaskID(id)] = true
	}
	return m
}

// TestConditionEval exercises the evaluator's truth table across every
// supported operator: scalar ==/!=, numeric </>, contains, and the
// succeeded/failed status helpers. The single assertion target is "Eval
// returns the expected boolean".
func TestConditionEval(t *testing.T) {
	cases := []struct {
		name string
		expr string
		env  workflow.Env
		want bool
	}{
		{
			name: "eq matches",
			expr: `{{review}} == "LGTM"`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"review": "LGTM"}},
			want: true,
		},
		{
			name: "eq differs",
			expr: `{{review}} == "LGTM"`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"review": "nope"}},
			want: false,
		},
		{
			name: "neq differs",
			expr: `{{review}} != "LGTM"`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"review": "nope"}},
			want: true,
		},
		{
			name: "gt above",
			expr: `{{count}} > 3`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"count": "5"}},
			want: true,
		},
		{
			name: "gt below",
			expr: `{{count}} > 3`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"count": "2"}},
			want: false,
		},
		{
			name: "lt below",
			expr: `{{count}} < 3`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"count": "2"}},
			want: true,
		},
		{
			name: "contains present",
			expr: `contains({{log}}, "error")`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"log": "an error happened"}},
			want: true,
		},
		{
			name: "contains absent",
			expr: `contains({{log}}, "error")`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"log": "all good"}},
			want: false,
		},
		{
			name: "succeeded true",
			expr: `succeeded(build)`,
			env:  workflow.Env{Succeeded: map[workflow.TaskID]bool{"build": true}},
			want: true,
		},
		{
			name: "succeeded false",
			expr: `succeeded(build)`,
			env:  workflow.Env{Succeeded: map[workflow.TaskID]bool{"build": false}},
			want: false,
		},
		{
			name: "failed true",
			expr: `failed(build)`,
			env:  workflow.Env{Succeeded: map[workflow.TaskID]bool{"build": false}},
			want: true,
		},
		{
			name: "failed false",
			expr: `failed(build)`,
			env:  workflow.Env{Succeeded: map[workflow.TaskID]bool{"build": true}},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cond, err := workflow.ParseCondition(tc.expr, known("review", "count", "log", "build"))
			if err != nil {
				t.Fatalf("ParseCondition(%q) returned error: %v", tc.expr, err)
			}
			got, err := cond.Eval(tc.env)
			if err != nil {
				t.Fatalf("Eval returned error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Eval(%q) = %v, want %v", tc.expr, got, tc.want)
			}
		})
	}
}

// TestParseCondition_AcceptsValidExpr pins that a well-formed expression whose
// references are all known compiles without error.
func TestParseCondition_AcceptsValidExpr(t *testing.T) {
	cond, err := workflow.ParseCondition(`{{a}} == "x"`, known("a"))
	if err != nil {
		t.Fatalf("ParseCondition returned error: %v", err)
	}
	if cond == nil {
		t.Fatal("ParseCondition returned nil condition for a valid expression")
	}
}

// TestParseCondition_RejectsUnknownPlaceholderRef pins that a placeholder
// naming an id absent from the known set is an UnknownConditionRefError.
func TestParseCondition_RejectsUnknownPlaceholderRef(t *testing.T) {
	_, err := workflow.ParseCondition(`{{ghost}} == "x"`, known("a"))
	var got *workflow.UnknownConditionRefError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownConditionRefError failed; err = %v", err)
	}
	if got.Ref != "ghost" {
		t.Errorf("UnknownConditionRefError.Ref = %q, want ghost", got.Ref)
	}
}

// TestParseCondition_RejectsUnknownHelperRef pins that a status helper naming an
// unknown id is an UnknownConditionRefError.
func TestParseCondition_RejectsUnknownHelperRef(t *testing.T) {
	_, err := workflow.ParseCondition(`succeeded(ghost)`, known("a"))
	var got *workflow.UnknownConditionRefError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownConditionRefError failed; err = %v", err)
	}
}

// TestParseCondition_RejectsMalformedExpr pins that a syntactically invalid
// expression is a MalformedConditionError.
func TestParseCondition_RejectsMalformedExpr(t *testing.T) {
	_, err := workflow.ParseCondition(`{{a}} === "x"`, known("a"))
	var got *workflow.MalformedConditionError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As MalformedConditionError failed; err = %v", err)
	}
}

// TestParseCondition_RejectsEmptyExpr pins that an empty expression is rejected
// as malformed rather than silently treated as always-true.
func TestParseCondition_RejectsEmptyExpr(t *testing.T) {
	_, err := workflow.ParseCondition(``, known("a"))
	var got *workflow.MalformedConditionError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As MalformedConditionError failed; err = %v", err)
	}
}
