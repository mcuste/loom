package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// waitWindow bounds how long a teatest assertion waits for the live model to
// reach the expected state before failing. It is generous enough to absorb the
// program's startup and message dispatch yet short enough that the stubbed
// model fails fast rather than hanging the suite.
const waitWindow = 3 * time.Second

// newLiveModel spins up a teatest harness over a runModel with the given total
// denominator and a no-op cancel func. The fixed 80x24 term size keeps the
// View's height bound deterministic.
func newLiveModel(t *testing.T, total int) *teatest.TestModel {
	t.Helper()
	m := newRunModel(RunMeta{RunFile: "/r", Cwd: "/c", Total: total}, func() {})
	return teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
}

// finishMsg builds a successful LLM finish message carrying usage so tests can
// assert the committed scrollback summary reports tokens and cost.
func finishMsg(id workflow.TaskID, in, out, cache int, cost float64) taskFinishMsg {
	return taskFinishMsg{
		id: id,
		res: executor.TaskResult{
			TaskID: id,
			Usage:  runtime.Usage{InputTokens: in, OutputTokens: out, CacheReadTokens: cache, TotalCostUSD: cost},
		},
	}
}

// TestRunModel_ViewShowsRunningTasks pins that a task moved to running by a
// start message appears in the live View region.
func TestRunModel_ViewShowsRunningTasks(t *testing.T) {
	tm := newLiveModel(t, 2)
	tm.Send(taskStartMsg{id: "draft", rt: "claude-code", model: "opus", effort: "high"})
	tm.Send(taskStartMsg{id: "review", rt: "claude-code", model: "opus", effort: "high"})

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("draft")) && bytes.Contains(b, []byte("review"))
	}, teatest.WithDuration(waitWindow))

	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(waitWindow))
}

// TestRunModel_FinishCommitsSummaryToScrollback pins that finishing a task
// commits a permanent one-line summary (reporting its tokens and cost) to
// scrollback, which the View region for running tasks would never print.
func TestRunModel_FinishCommitsSummaryToScrollback(t *testing.T) {
	tm := newLiveModel(t, 1)
	tm.Send(taskStartMsg{id: "draft", rt: "claude-code", model: "opus", effort: "high"})
	tm.Send(finishMsg("draft", 1, 2, 3, 0.00001))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("draft")) && bytes.Contains(b, []byte("$0.000010"))
	}, teatest.WithDuration(waitWindow))

	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(waitWindow))
}

// TestRunModel_StatusBarShowsCompletedTotals pins that the status bar reports
// the completed-over-total counter as tasks finish: after two of three tasks
// finish, the bar reads "2/3".
func TestRunModel_StatusBarShowsCompletedTotals(t *testing.T) {
	tm := newLiveModel(t, 3)
	tm.Send(taskStartMsg{id: "a", rt: "claude-code", model: "opus"})
	tm.Send(finishMsg("a", 1, 1, 0, 0.0))
	tm.Send(taskStartMsg{id: "b", rt: "claude-code", model: "opus"})
	tm.Send(finishMsg("b", 1, 1, 0, 0.0))

	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("2/3"))
	}, teatest.WithDuration(waitWindow))

	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(waitWindow))
}

// TestRunModel_ViewBoundsHeightWithOverflow pins the height-overflow branch in
// View: when more tasks run than fit in the height budget, the region is capped
// and a single "… N more running" line accounts for the remainder.
func TestRunModel_ViewBoundsHeightWithOverflow(t *testing.T) {
	m := newRunModel(RunMeta{Total: 3}, func() {})
	m.height = 2 // budget of 1 row for running tasks, the rest overflows
	m.onStart(taskStartMsg{id: "a", rt: "claude-code", model: "opus"})
	m.onStart(taskStartMsg{id: "b", rt: "claude-code", model: "opus"})
	m.onStart(taskStartMsg{id: "c", rt: "claude-code", model: "opus"})

	out := m.View()
	if !strings.Contains(out, "3 more running") {
		t.Fatalf("expected overflow line for 3 running tasks, got %q", out)
	}
}

// TestFinishLine_ShellPaths pins the shell branches of finishLine: a successful
// shell task reports exit=0 (no token/cost columns), while a failed one reports
// FAIL with the error, diverging from the LLM finish format.
func TestFinishLine_ShellPaths(t *testing.T) {
	sym := symbolsFor(termenv.TrueColor)
	ok := finishLine(taskFinishMsg{
		id:    "build",
		shell: true,
		res:   executor.TaskResult{Elapsed: 1500 * time.Millisecond},
	}, sym)
	if !strings.Contains(ok, "exit=0") || strings.Contains(ok, "in=") {
		t.Fatalf("shell success line = %q, want exit=0 and no token columns", ok)
	}

	fail := finishLine(taskFinishMsg{
		id:    "build",
		shell: true,
		err:   errors.New("boom"),
		res:   executor.TaskResult{Elapsed: time.Second},
	}, sym)
	if !strings.Contains(fail, "FAIL") || !strings.Contains(fail, "boom") {
		t.Fatalf("shell failure line = %q, want FAIL and the error", fail)
	}
}

// TestFinishLine_AppendsIterSuffix pins that finishLine annotates a looped
// task's committed scrollback line with its 1-based pass ("iter 2") while a
// non-looped task (iter 0) stays byte-identical, with no trailing annotation.
func TestFinishLine_AppendsIterSuffix(t *testing.T) {
	sym := symbolsFor(termenv.TrueColor)

	looped := finishLine(taskFinishMsg{
		id:    "draft",
		iter:  2,
		shell: true,
		res:   executor.TaskResult{Elapsed: time.Second},
	}, sym)
	if !strings.Contains(looped, "iter 2") {
		t.Fatalf("looped finish line = %q, want it to contain %q", looped, "iter 2")
	}

	plain := finishLine(taskFinishMsg{
		id:    "draft",
		iter:  0,
		shell: true,
		res:   executor.TaskResult{Elapsed: time.Second},
	}, sym)
	if strings.Contains(plain, "iter") {
		t.Fatalf("non-looped finish line = %q, want no iter annotation", plain)
	}
}

// TestDescriptor_ShellVsLLM pins descriptor's shell branch ("(shell)") against
// the LLM branch that renders the runtime/model facts.
func TestDescriptor_ShellVsLLM(t *testing.T) {
	if got := descriptor(&liveTask{id: "build", shell: true}); got != "(shell)" {
		t.Fatalf("shell descriptor = %q, want (shell)", got)
	}
	llm := descriptor(&liveTask{id: "draft", rt: "claude-code", model: "opus", effort: "high"})
	if !strings.Contains(llm, "claude-code") || !strings.Contains(llm, "opus") {
		t.Fatalf("llm descriptor = %q, want runtime and model facts", llm)
	}
}

// TestRunModel_CtrlCCancelsRun pins that ctrl-c cancels the caller's run
// context: the model is built with a real cancel func, and pressing ctrl-c
// must close the context's Done channel.
func TestRunModel_CtrlCCancelsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newRunModel(RunMeta{RunFile: "/r", Cwd: "/c", Total: 1}, cancel)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})

	select {
	case <-ctx.Done():
		// cancelled as expected
	case <-time.After(waitWindow):
		t.Fatal("ctrl-c did not cancel the run context")
	}

	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(waitWindow))
}
