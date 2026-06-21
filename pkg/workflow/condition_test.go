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
			name: "gt at boundary",
			expr: `{{count}} > 3`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"count": "3"}},
			want: false,
		},
		{
			name: "lt at boundary",
			expr: `{{count}} < 3`,
			env:  workflow.Env{Outputs: map[workflow.TaskID]string{"count": "3"}},
			want: false,
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
			// failed(id) is true when the task is absent from both Succeeded and
			// Skipped: it neither completed successfully nor was guarded out. This
			// is the realistic production state for a failure (the executor only
			// ever writes succeeded[id]=true or skipped[id]=true), unlike a
			// Succeeded[id]=false entry which is never produced.
			name: "failed true",
			expr: `failed(build)`,
			env:  workflow.Env{},
			want: true,
		},
		{
			name: "failed false",
			expr: `failed(build)`,
			env:  workflow.Env{Succeeded: map[workflow.TaskID]bool{"build": true}},
			want: false,
		},
		{
			// A task skipped by its own when: guard is neither succeeded nor
			// failed: failed(id) must stay false so a failure-branch does not fire
			// on a skip.
			name: "failed false when skipped",
			expr: `failed(build)`,
			env:  workflow.Env{Skipped: map[workflow.TaskID]bool{"build": true}},
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

// TestConditionEval_NonIntegerOutputErrors pins that a numeric comparison
// against an output that does not parse as an integer is an Eval error, not a
// silent false. This is the only Eval path that returns a non-nil error.
func TestConditionEval_NonIntegerOutputErrors(t *testing.T) {
	cond, err := workflow.ParseCondition(`{{count}} < 3`, known("count"))
	if err != nil {
		t.Fatalf("ParseCondition returned error: %v", err)
	}
	_, err = cond.Eval(workflow.Env{Outputs: map[workflow.TaskID]string{"count": "not-a-number"}})
	if err == nil {
		t.Fatal("Eval returned nil error for a non-integer output, want error")
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
