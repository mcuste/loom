package workflow_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParseOkExitBodyForms pins that ok_exit parses on the process-running body
// forms (command, script, LLM) and decodes into the typed slice.
func TestParseOkExitBodyForms(t *testing.T) {
	tests := map[string]string{
		"command": "name: wf\ntasks:\n  - id: a\n    command: \"exit 1\"\n    ok_exit: [0, 1]\n",
		"script":  "name: wf\ntasks:\n  - id: a\n    script: ./x.sh\n    ok_exit: [0, 2]\n",
		"llm":     "name: wf\nruntime: rt\nmodel: m1\ntasks:\n  - id: a\n    prompt: hi\n    ok_exit: [1]\n",
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			wf, err := workflow.Parse([]byte(src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(wf.Tasks[0].OkExit) == 0 {
				t.Errorf("OkExit not decoded: %v", wf.Tasks[0].OkExit)
			}
		})
	}
}

// TestParseOkExitRejectedOnSubWorkflow pins that ok_exit is rejected on a
// sub-workflow task (no process exit).
func TestParseOkExitRejectedOnSubWorkflow(t *testing.T) {
	src := "name: wf\ntasks:\n  - id: a\n    workflow: child\n    ok_exit: [1]\n"
	_, err := workflow.Parse([]byte(src))
	if !errors.Is(err, workflow.ErrOkExitOnSubWorkflow) {
		t.Fatalf("error = %v, want ErrOkExitOnSubWorkflow", err)
	}
}

// TestParseOkExitOutOfRange pins that exit codes outside 0-255 are rejected.
func TestParseOkExitOutOfRange(t *testing.T) {
	for _, src := range []string{
		"name: wf\ntasks:\n  - id: a\n    command: \"true\"\n    ok_exit: [256]\n",
		"name: wf\ntasks:\n  - id: a\n    command: \"true\"\n    ok_exit: [-1]\n",
	} {
		_, err := workflow.Parse([]byte(src))
		if !errors.Is(err, workflow.ErrOkExitOutOfRange) {
			t.Fatalf("error = %v, want ErrOkExitOutOfRange for %q", err, src)
		}
	}
}

// TestParseOkExitRejectedOnLoopWrapper pins that a loop-wrapper task may not set
// ok_exit (it belongs to the body tasks).
func TestParseOkExitRejectedOnLoopWrapper(t *testing.T) {
	src := `
name: wf
tasks:
  - id: loopw
    ok_exit: [1]
    for_each:
      in: [a, b]
      as: x
      tasks:
        - id: body
          command: "echo {{x}}"
`
	_, err := workflow.Parse([]byte(src))
	if !errors.Is(err, workflow.ErrLoopTaskWithFields) {
		t.Fatalf("error = %v, want ErrLoopTaskWithFields", err)
	}
}

// TestParseOkExitValueSurvives pins the exact decoded slice for a representative
// task.
func TestParseOkExitValueSurvives(t *testing.T) {
	src := "name: wf\ntasks:\n  - id: a\n    command: \"exit 2\"\n    ok_exit: [0, 2, 13]\n"
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !slices.Equal(wf.Tasks[0].OkExit, []int{0, 2, 13}) {
		t.Errorf("OkExit = %v, want [0 2 13]", wf.Tasks[0].OkExit)
	}
}
