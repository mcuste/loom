package workflow_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestValidRetryClass_AcceptsTransient pins that the transient class name is
// recognized by the vocabulary predicate.
func TestValidRetryClass_AcceptsTransient(t *testing.T) {
	if !workflow.ValidRetryClass("transient") {
		t.Errorf("ValidRetryClass(%q) = false, want true", "transient")
	}
}

// TestValidRetryClass_RejectsUnknownClass pins that an unrecognized class name
// is rejected by the predicate.
func TestValidRetryClass_RejectsUnknownClass(t *testing.T) {
	if workflow.ValidRetryClass("permanent") {
		t.Errorf("ValidRetryClass(%q) = true, want false", "permanent")
	}
}

// TestRetryClassTransient_HasTransientValue pins the const value so the
// vocabulary stays the single source of truth for the bare class name.
func TestRetryClassTransient_HasTransientValue(t *testing.T) {
	if got := string(workflow.RetryClassTransient); got != "transient" {
		t.Errorf("RetryClassTransient = %q, want %q", got, "transient")
	}
}
