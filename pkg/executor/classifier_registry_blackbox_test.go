package executor_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestHasClassifier_CoversEveryValidRetryClass is the drift guard between the
// parse vocabulary and the runtime registry, expressed through the public API
// so it survives any refactor of the unexported `classifiers` map. Every class
// the parser admits must have a registered classifier; otherwise parse could
// accept a class the runtime would silently ignore.
func TestHasClassifier_CoversEveryValidRetryClass(t *testing.T) {
	t.Parallel()
	for _, class := range workflow.RetryClasses() {
		if !executor.HasClassifier(class) {
			t.Errorf("no classifier registered for valid retry class %q", class)
		}
	}
}

// TestHasClassifier_RejectsUnknownClass pins that a class outside the parse
// vocabulary has no registered classifier, so the registry never claims to
// handle a class ValidRetryClass would reject.
func TestHasClassifier_RejectsUnknownClass(t *testing.T) {
	t.Parallel()
	unknown := workflow.RetryClass("definitely-not-a-real-class")

	if executor.HasClassifier(unknown) {
		t.Errorf("HasClassifier(%q) = true, want false for an unknown class", unknown)
	}
}

// TestClassifierClasses_AreAllValidRetryClasses is the reverse drift guard:
// every registered classifier must name a class the parser admits. Without it,
// a future invalid entry in the classifiers map would go undetected because the
// forward guard only checks that each valid class is covered.
func TestClassifierClasses_AreAllValidRetryClasses(t *testing.T) {
	t.Parallel()
	for _, class := range executor.ClassifierClasses() {
		if !workflow.ValidRetryClass(class) {
			t.Errorf("classifier registered for invalid retry class %q", class)
		}
	}
}
