package tui_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestPlainRenderer_AnnotatesLoopIteration pins that the plain renderer marks a
// looped task's progress line with its 1-based iteration: when OnStart fires
// with iter=2, the emitted line carries an "iter 2" annotation so a per-loop
// pass is distinguishable in non-TTY output.
func TestPlainRenderer_AnnotatesLoopIteration(t *testing.T) {
	llm := workflow.Task{ID: "draft", Prompt: "hi"}

	var buf bytes.Buffer
	r := tui.New(&buf)
	if err := r.Header(runner.RunMeta{RunFile: "/r", Cwd: "/c", Total: 3}); err != nil {
		t.Fatalf("Header: %v", err)
	}
	buf.Reset() // drop the header; assert only the progress line

	r.Hooks().OnStart(llm, 2, "claude-code", "opus", "high")

	if got := buf.String(); !strings.Contains(got, "iter 2") {
		t.Errorf("looped start line = %q, want it to contain %q", got, "iter 2")
	}
}
