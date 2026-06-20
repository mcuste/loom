package main

import (
	"bytes"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestHooksOnStart_DiscriminatesShellViaIsShell pins that the progress hook's
// OnStart line is selected by workflow.Task.IsShell(), not by the routing
// fields being empty.
//
// The decisive case feeds a shell task (Command set) while passing NON-empty
// runtime/model/effort to OnStart: a hook that keys off IsShell renders the
// shell line, whereas one that keys off `rt == ""` would wrongly render the
// LLM line.
func TestHooksOnStart_DiscriminatesShellViaIsShell(t *testing.T) {
	cases := []struct {
		name   string
		task   workflow.Task
		rt     runtime.Name
		model  runtime.Model
		effort runtime.Effort
		want   string
	}{
		{
			name: "shell task with empty routing fields renders shell line",
			task: workflow.Task{ID: "build", Command: "make"},
			want: "[1/3] build (shell)\n",
		},
		{
			name:   "shell task still renders shell line despite non-empty routing fields",
			task:   workflow.Task{ID: "build", Command: "make"},
			rt:     "claude-code",
			model:  "opus",
			effort: "high",
			want:   "[1/3] build (shell)\n",
		},
		{
			name:   "llm task renders runtime/model/effort line",
			task:   workflow.Task{ID: "draft", Prompt: "hi"},
			rt:     "claude-code",
			model:  "opus",
			effort: "high",
			want:   "[1/3] draft (claude-code/opus/high)\n",
		},
		{
			name:  "llm task without effort omits the effort suffix",
			task:  workflow.Task{ID: "draft", Prompt: "hi"},
			rt:    "claude-code",
			model: "opus",
			want:  "[1/3] draft (claude-code/opus)\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h := hooks(&buf, 3)
			h.OnStart(tc.task, tc.rt, tc.model, tc.effort)
			if got := buf.String(); got != tc.want {
				t.Errorf("OnStart line = %q, want %q", got, tc.want)
			}
		})
	}
}
