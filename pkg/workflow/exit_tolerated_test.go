package workflow_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestExitTolerated pins the success-vs-failure rule per body form: 0 is always
// success, a signal kill (-1) never is, a script tolerates every code by default
// while command/LLM tolerate only 0, and an ok_exit list is the exact non-zero
// success set for any form.
func TestExitTolerated(t *testing.T) {
	cmd := workflow.Task{ID: "a", Command: "x"}
	script := workflow.Task{ID: "a", Script: "x"}
	llm := workflow.Task{ID: "a", Prompt: "x"}
	cmdOk := workflow.Task{ID: "a", Command: "x", OkExit: []int{1, 2}}
	llmOk := workflow.Task{ID: "a", Prompt: "x", OkExit: []int{1}}
	scriptOk := workflow.Task{ID: "a", Script: "x", OkExit: []int{2}}

	tests := []struct {
		name string
		task workflow.Task
		code int
		want bool
	}{
		{"zero always ok (cmd)", cmd, 0, true},
		{"zero always ok (llm)", llm, 0, true},
		{"signal kill never ok (script)", script, -1, false},
		{"signal kill never ok (cmdOk)", cmdOk, -1, false},
		{"command default fails non-zero", cmd, 1, false},
		{"llm default fails non-zero", llm, 1, false},
		{"script default tolerates all", script, 1, true},
		{"script default tolerates high code", script, 42, true},
		{"ok_exit list tolerates listed (cmd)", cmdOk, 2, true},
		{"ok_exit list rejects unlisted (cmd)", cmdOk, 3, false},
		{"ok_exit list tolerates listed (llm)", llmOk, 1, true},
		{"ok_exit list rejects unlisted (llm)", llmOk, 2, false},
		{"script ok_exit restricts to list", scriptOk, 1, false},
		{"script ok_exit tolerates listed", scriptOk, 2, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.task.ExitTolerated(tc.code); got != tc.want {
				t.Errorf("ExitTolerated(%d) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}
