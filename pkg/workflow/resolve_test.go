package workflow_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

func TestEffectiveFallsBackToWorkflow(t *testing.T) {
	wf := &workflow.Workflow{
		Runtime: "test-rt",
		Model:   "m1",
		Effort:  "low",
	}
	t1 := workflow.Task{ID: "a"}
	rt, m, e := wf.Effective(&t1)
	if rt != runtime.Name("test-rt") || m != runtime.Model("m1") || e != runtime.Effort("low") {
		t.Fatalf("Effective = (%q,%q,%q), want (test-rt, m1, low)", rt, m, e)
	}
}

func TestEffectiveTaskOverrides(t *testing.T) {
	wf := &workflow.Workflow{
		Runtime: "test-rt",
		Model:   "m1",
		Effort:  "low",
	}
	t1 := workflow.Task{ID: "a", Model: "m2", Effort: "high"}
	rt, m, e := wf.Effective(&t1)
	if rt != runtime.Name("test-rt") || m != runtime.Model("m2") || e != runtime.Effort("high") {
		t.Fatalf("Effective = (%q,%q,%q), want (test-rt, m2, high)", rt, m, e)
	}
}

func TestSubstitute(t *testing.T) {
	outputs := map[workflow.TaskID]string{
		"a": "Apple",
		"b": "Banana",
	}
	tests := []struct {
		name, in, want string
	}{
		{"single placeholder", "got {{a}}", "got Apple"},
		{"two placeholders", "{{a}} and {{b}}", "Apple and Banana"},
		{"repeated placeholder", "{{a}} {{a}}", "Apple Apple"},
		{"no placeholders", "hello world", "hello world"},
		{"unknown placeholder kept", "got {{c}}", "got {{c}}"},
		{"adjacent text preserved", "x{{a}}y", "xAppley"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := workflow.Substitute(tc.in, outputs)
			if got != tc.want {
				t.Fatalf("Substitute(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
