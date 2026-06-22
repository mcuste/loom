package tui_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// bar is the 40-rune rule the summary block draws above and below the totals.
// Built with strings.Repeat so the test pins the count rather than a literal a
// reviewer cannot eyeball.
var bar = strings.Repeat("─", 40)

// parseWF parses a manifest for a parity test, failing the test on error.
func parseWF(t *testing.T, body string) *workflow.Workflow {
	t.Helper()
	wf, err := workflow.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return wf
}

// TestPlainRenderer_HeaderPlainRun pins the plain header for a fresh, non-loop,
// non-seeded run: just the run-file and cwd lines, the second followed by a
// blank line.
func TestPlainRenderer_HeaderPlainRun(t *testing.T) {
	var buf bytes.Buffer
	r := tui.New(&buf)
	if err := r.Header(tui.RunMeta{RunFile: "/runs/wf/abc.json", Cwd: "/work", Total: 2}); err != nil {
		t.Fatalf("Header: %v", err)
	}

	want := "Run file : /runs/wf/abc.json\n" +
		"Cwd      : /work\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Header() = %q, want %q", got, want)
	}
}

// TestPlainRenderer_HeaderSeeded pins that a non-zero seeded count appends the
// "Seeded : N task(s)" line after the cwd block.
func TestPlainRenderer_HeaderSeeded(t *testing.T) {
	var buf bytes.Buffer
	r := tui.New(&buf)
	if err := r.Header(tui.RunMeta{RunFile: "/runs/wf/abc.json", Cwd: "/work", Seeded: 2, Total: 1}); err != nil {
		t.Fatalf("Header: %v", err)
	}

	want := "Run file : /runs/wf/abc.json\n" +
		"Cwd      : /work\n\n" +
		"Seeded   : 2 task(s) from prior run\n\n"
	if got := buf.String(); got != want {
		t.Errorf("Header() = %q, want %q", got, want)
	}
}

// TestPlainRenderer_PlanShellTask pins the printPlan layout for a single shell
// task: header rows with dashes for the absent runtime/model/effort, then the
// numbered execution line with kind=shell and a quoted command.
func TestPlainRenderer_PlanShellTask(t *testing.T) {
	wf := parseWF(t, "name: demo\ntasks:\n  - id: greet\n    command: echo hi\n")

	var buf bytes.Buffer
	r := tui.New(&buf)
	if err := r.Plan(wf, nil, nil, nil); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	want := "Workflow : demo\n" +
		"Runtime  : -\n" +
		"Model    : -\n" +
		"Effort   : -\n" +
		"\n" +
		"Execution order (1 task):\n" +
		"   1. greet  kind=shell  cmd=\"echo hi\"  deps=none\n"
	if got := buf.String(); got != want {
		t.Errorf("Plan() = %q, want %q", got, want)
	}
}

// TestPlainRenderer_SummaryComplete pins the success summary block: the boxed
// totals followed by the ✓ "complete" line when the completed count equals the
// expected count.
func TestPlainRenderer_SummaryComplete(t *testing.T) {
	wf := parseWF(t, "name: demo\ntasks:\n  - id: a\n    command: echo a\n  - id: b\n    command: echo b\n")
	rep := &executor.Report{
		Usage: runtime.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, TotalCostUSD: 0.123456},
		Tasks: []executor.TaskResult{{TaskID: "a"}, {TaskID: "b"}},
	}

	var buf bytes.Buffer
	r := tui.New(&buf)
	if err := r.Summary(wf, rep, 2); err != nil {
		t.Fatalf("Summary: %v", err)
	}

	want := "\n" + bar + "\n" +
		"  total tokens : 10 in / 20 out / 5 cache-read\n" +
		"  total cost   : $0.123456\n" +
		bar + "\n" +
		"✓ workflow \"demo\" complete\n"
	if got := buf.String(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// TestPlainRenderer_SummaryPartial pins the partial-failure summary line: when
// fewer tasks completed than expected, the ✗ "stopped after m/n" line replaces
// the success line.
func TestPlainRenderer_SummaryPartial(t *testing.T) {
	wf := parseWF(t, "name: demo\ntasks:\n  - id: a\n    command: echo a\n  - id: b\n    command: echo b\n")
	rep := &executor.Report{
		Usage: runtime.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 0, TotalCostUSD: 0.000001},
		Tasks: []executor.TaskResult{{TaskID: "a"}},
	}

	var buf bytes.Buffer
	r := tui.New(&buf)
	if err := r.Summary(wf, rep, 2); err != nil {
		t.Fatalf("Summary: %v", err)
	}

	want := "\n" + bar + "\n" +
		"  total tokens : 1 in / 2 out / 0 cache-read\n" +
		"  total cost   : $0.000001\n" +
		bar + "\n" +
		"✗ workflow \"demo\" stopped after 1/2 tasks\n"
	if got := buf.String(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// TestPlainRenderer_SummaryLoopCountsDistinct pins that a looping run reports
// success: a scoped loop records one rep.Tasks entry per iteration, so the raw
// count (4) exceeds the expected task count (2); the summary must collapse the
// repeated member to a distinct count and print the ✓ line, not a false stop.
func TestPlainRenderer_SummaryLoopCountsDistinct(t *testing.T) {
	wf := parseWF(t, "name: demo\ntasks:\n  - id: a\n    command: echo a\n  - id: b\n    command: echo b\n")
	rep := &executor.Report{
		Usage: runtime.Usage{},
		Tasks: []executor.TaskResult{{TaskID: "a"}, {TaskID: "b"}, {TaskID: "b"}, {TaskID: "b"}},
	}

	var buf bytes.Buffer
	if err := tui.New(&buf).Summary(wf, rep, 2); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "✓ workflow \"demo\" complete") {
		t.Errorf("Summary() = %q, want it to contain the complete line", got)
	}
}

// TestPlainRenderer_HooksRenderProgressLines pins the per-task progress lines
// emitted through the renderer's Hooks. The denominator comes from
// RunMeta.Total set by a prior Header call; the buffer is reset after the
// header so each case asserts only its own line. Shell vs LLM flavour is keyed
// off Task.IsShell, matching executor's hook contract.
func TestPlainRenderer_HooksRenderProgressLines(t *testing.T) {
	llm := workflow.Task{ID: "draft", Prompt: "hi"}
	shell := workflow.Task{ID: "build", Command: "make"}

	cases := []struct {
		name string
		call func(h executor.Hooks)
		want string
	}{
		{
			name: "llm start",
			call: func(h executor.Hooks) { h.OnStart(llm, 0, "claude-code", "opus", "high") },
			want: "[1/3] draft (claude-code/opus/high)\n",
		},
		{
			name: "llm start without effort omits suffix",
			call: func(h executor.Hooks) { h.OnStart(llm, 0, "claude-code", "opus", "") },
			want: "[1/3] draft (claude-code/opus)\n",
		},
		{
			name: "shell start",
			call: func(h executor.Hooks) { h.OnStart(shell, 0, "", "", "") },
			want: "[1/3] build (shell)\n",
		},
		{
			name: "llm finish reports tokens and cost",
			call: func(h executor.Hooks) {
				h.OnFinish(llm, 0, executor.TaskResult{
					Usage: runtime.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, TotalCostUSD: 0.00001},
				}, nil)
			},
			want: "  draft done 0s  in=1 out=2 cache=3  $0.000010\n",
		},
		{
			name: "shell finish reports exit zero",
			call: func(h executor.Hooks) { h.OnFinish(shell, 0, executor.TaskResult{}, nil) },
			want: "  build done 0s  exit=0\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := tui.New(&buf)
			r.Header(tui.RunMeta{RunFile: "/r", Cwd: "/c", Total: 3})
			buf.Reset() // drop the header; assert only the progress line
			tc.call(r.Hooks())
			if got := buf.String(); got != tc.want {
				t.Errorf("hook line = %q, want %q", got, tc.want)
			}
		})
	}
}
