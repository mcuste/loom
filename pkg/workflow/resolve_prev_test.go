package workflow_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestSubstitutePrevHit pins that a `{{prev.id}}` placeholder resolves to the
// previous loop-iteration output supplied via the prev map.
func TestSubstitutePrevHit(t *testing.T) {
	prev := map[workflow.TaskID]string{"draft": "v1"}
	got := workflow.Substitute("refine {{prev.draft}}", nil, nil, nil, prev, nil)
	if got != "refine v1" {
		t.Fatalf("Substitute = %q, want %q", got, "refine v1")
	}
}

// TestSubstitutePrevMissCollapsesToEmpty pins the first-iteration semantics: a
// prev key with no entry substitutes to the empty string, mirroring state keys
// rather than being left verbatim like an unknown task/param placeholder.
func TestSubstitutePrevMissCollapsesToEmpty(t *testing.T) {
	got := workflow.Substitute("before {{prev.draft}} after", nil, nil, nil, nil, nil)
	if got != "before  after" {
		t.Fatalf("Substitute = %q, want %q", got, "before  after")
	}
}

// TestSubstitutePrevDoesNotCollideWithTaskNamespace pins that prev resolves
// from the prev map only: a bare `{{draft}}` task placeholder and a
// `{{prev.draft}}` prev placeholder for the same id resolve independently in a
// single pass.
func TestSubstitutePrevDoesNotCollideWithTaskNamespace(t *testing.T) {
	outputs := map[workflow.TaskID]string{"draft": "current"}
	prev := map[workflow.TaskID]string{"draft": "previous"}
	got := workflow.Substitute("{{draft}}/{{prev.draft}}", outputs, nil, nil, prev, nil)
	if got != "current/previous" {
		t.Fatalf("Substitute = %q, want %q (prev must not collide with task namespace)", got, "current/previous")
	}
}

// TestSubstitutePrevDoesNotCollideWithParamNamespace pins that a `prev` key and
// a `params` key sharing the same name resolve from their own maps.
func TestSubstitutePrevDoesNotCollideWithParamNamespace(t *testing.T) {
	params := workflow.ParamValues{"draft": "param-val"}
	prev := map[workflow.TaskID]string{"draft": "prev-val"}
	got := workflow.Substitute("{{params.draft}}/{{prev.draft}}", nil, params, nil, prev, nil)
	if got != "param-val/prev-val" {
		t.Fatalf("Substitute = %q, want %q (prev must not collide with params namespace)", got, "param-val/prev-val")
	}
}

// TestSubstitutePrevNoDoubleExpansion pins the primary single-pass invariant for
// the prev namespace: a prev value containing placeholder text is not
// re-expanded against the task/param maps in the same pass.
func TestSubstitutePrevNoDoubleExpansion(t *testing.T) {
	outputs := map[workflow.TaskID]string{"a": "Apple"}
	prev := map[workflow.TaskID]string{"x": "{{a}}"}
	got := workflow.Substitute("got {{prev.x}}", outputs, nil, nil, prev, nil)
	if got != "got {{a}}" {
		t.Fatalf("Substitute = %q, want %q (prev value must not be re-expanded)", got, "got {{a}}")
	}
}
