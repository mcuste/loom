package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// taskStartMsg and taskFinishMsg are the bubbletea messages the ttyRenderer's
// Hooks forward into the running program via Program.Send. OnStart maps to a
// taskStartMsg, OnFinish to a taskFinishMsg; the live model consumes them to
// move a task between its pending/running/done/failed buckets.
type taskStartMsg struct {
	id     workflow.TaskID
	rt     runtime.Name
	model  runtime.Model
	effort runtime.Effort
	shell  bool
}

type taskFinishMsg struct {
	id    workflow.TaskID
	res   executor.TaskResult
	err   error
	shell bool
}

// taskState is a task's position in its lifecycle within the live view.
type taskState int

const (
	stateRunning taskState = iota
	stateDone
	stateFailed
)

// liveTask is the per-task record the model keeps while a task is in flight. It
// holds enough to render the spinner line and, on finish, the committed
// scrollback summary.
type liveTask struct {
	id     workflow.TaskID
	rt     runtime.Name
	model  runtime.Model
	effort runtime.Effort
	shell  bool
	state  taskState
}

// runModel is the bubbletea model for the live `loom run` region. It tracks
// per-task state plus the running totals shown in the status bar (tasks
// completed, tokens, cost, elapsed) and commits a permanent one-line summary
// to scrollback as each task finishes.
type runModel struct {
	cancel  context.CancelFunc
	spinner spinner.Model

	total   int // status-bar denominator (RunMeta.Total)
	done    int // tasks finished so far
	running int // tasks currently in flight (status-bar count)

	// usage accumulates across finished tasks for the status bar.
	usage runtime.Usage

	order []workflow.TaskID         // running-task render order
	tasks map[workflow.TaskID]*liveTask

	started  time.Time
	width    int
	height   int
	quitting bool
}

// newRunModel builds the live model for one run. meta seeds the status-bar
// denominator (RunMeta.Total); cancel is invoked when the user presses q or
// ctrl-c so the run unwinds through the caller's signal-cancelled context. A
// nil cancel is tolerated (the key press then only quits the live view).
func newRunModel(meta RunMeta, cancel context.CancelFunc) *runModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return &runModel{
		cancel:  cancel,
		spinner: sp,
		total:   meta.Total,
		tasks:   make(map[workflow.TaskID]*liveTask),
		started: time.Now(),
		width:   80,
		height:  24,
	}
}

// Init starts the spinner tick.
func (m *runModel) Init() tea.Cmd {
	return m.spinner.Tick
}

// Update folds task start/finish messages and key presses into model state. A
// finished task is committed to scrollback via tea.Println and dropped from the
// live view; q and ctrl-c cancel the run and quit.
func (m *runModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case taskStartMsg:
		m.onStart(msg)
		return m, nil

	case taskFinishMsg:
		line := m.onFinish(msg)
		return m, tea.Println(line)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

// onStart moves a task into the running bucket, recording its runtime facts for
// the spinner line.
func (m *runModel) onStart(msg taskStartMsg) {
	if t, ok := m.tasks[msg.id]; ok {
		t.state = stateRunning
		return
	}
	m.order = append(m.order, msg.id)
	m.tasks[msg.id] = &liveTask{
		id:     msg.id,
		rt:     msg.rt,
		model:  msg.model,
		effort: msg.effort,
		shell:  msg.shell,
		state:  stateRunning,
	}
	m.running++
}

// onFinish accumulates the task's usage into the running totals, drops it from
// the live view, and returns the permanent one-line summary to commit to
// scrollback.
func (m *runModel) onFinish(msg taskFinishMsg) string {
	m.done++
	m.usage.InputTokens += msg.res.Usage.InputTokens
	m.usage.OutputTokens += msg.res.Usage.OutputTokens
	m.usage.CacheReadTokens += msg.res.Usage.CacheReadTokens
	m.usage.TotalCostUSD += msg.res.Usage.TotalCostUSD

	if t, ok := m.tasks[msg.id]; ok {
		if msg.err != nil {
			t.state = stateFailed
		} else {
			t.state = stateDone
		}
		m.running--
		m.dropTask(msg.id)
	}
	return finishLine(msg)
}

// dropTask removes a finished task from the running render order so the live
// view shows only in-flight work.
func (m *runModel) dropTask(id workflow.TaskID) {
	delete(m.tasks, id)
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
}

// View renders the in-flight tasks and the one-line status bar, bounded so it
// never exceeds the terminal height.
func (m *runModel) View() string {
	if m.quitting {
		return ""
	}

	status := m.statusBar()

	// Reserve the last terminal row for the status bar; the rest is the
	// running-task region.
	budget := max(m.height-1, 1)

	lines := make([]string, 0, len(m.order))
	for _, id := range m.order {
		t := m.tasks[id]
		if t == nil || t.state != stateRunning {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", m.spinner.View(), t.id, descriptor(t)))
	}

	if len(lines) > budget {
		// Keep room for a "… N more" overflow line so the region stays within
		// the height bound.
		shown := max(budget-1, 0)
		hidden := len(lines) - shown
		lines = append(lines[:shown], fmt.Sprintf("… %d more running", hidden))
	}

	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString(status)
	return b.String()
}

// statusBar is the one-line summary at the bottom of the live region:
// done/total, running count, accrued cost, and elapsed wall time.
func (m *runModel) statusBar() string {
	elapsed := time.Since(m.started).Round(time.Second)
	return fmt.Sprintf("%d/%d done · %d running · $%.6f · %s",
		m.done, m.total, m.running, m.usage.TotalCostUSD, elapsed)
}

// descriptor renders the runtime facts shown after a running task's id.
func descriptor(t *liveTask) string {
	if t.shell {
		return "(shell)"
	}
	return fmt.Sprintf("(%s/%s%s)", t.rt, t.model, effortSuffix(t.effort))
}

// finishLine builds the permanent scrollback summary for a finished task,
// mirroring the plain renderer's per-task finish line so scrollback reads the
// same whether or not the live view is active.
func finishLine(msg taskFinishMsg) string {
	id := msg.id
	elapsed := msg.res.Elapsed.Round(time.Millisecond)
	if msg.err != nil {
		return fmt.Sprintf("  %s FAIL after %s: %v", id, elapsed, msg.err)
	}
	if msg.shell {
		return fmt.Sprintf("  %s done %s  exit=0", id, elapsed)
	}
	u := msg.res.Usage
	return fmt.Sprintf("  %s done %s  in=%d out=%d cache=%d  $%.6f",
		id, elapsed, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.TotalCostUSD)
}

// ttyRenderer drives the live `loom run` region through a bubbletea program in
// inline mode (no alternate screen), so terminal scrollback is preserved. The
// non-live surfaces (header, plan, warnings, summary) reuse the plain renderer's
// text output, written above the live region; Hooks forward executor callbacks
// into the program as messages.
type ttyRenderer struct {
	w      io.Writer
	plain  plainRenderer
	cancel context.CancelFunc

	mu     sync.Mutex
	prog   *tea.Program
	done   chan struct{} // closed when the program goroutine exits
	runErr error         // error returned by prog.Run, surfaced via Close
}

// newTTYRenderer builds a live renderer for w. Pressing q or ctrl-c is wired to
// raise SIGINT at this process, matching the signal a terminal already delivers
// for ctrl-c, so the run unwinds through the caller's signal-cancelled context
// without the renderer needing a direct handle to it.
func newTTYRenderer(w io.Writer) *ttyRenderer {
	return &ttyRenderer{
		w:      w,
		plain:  plainRenderer{w: w},
		cancel: func() { _ = syscall.Kill(syscall.Getpid(), syscall.SIGINT) },
	}
}

// Header prints the header block once, above the live region, then starts the
// bubbletea program (idempotent across loop iterations).
func (t *ttyRenderer) Header(meta RunMeta) error {
	if err := t.plain.Header(meta); err != nil {
		return err
	}
	t.start(meta)
	return nil
}

// start launches the live program in a goroutine. The goroutine exits when the
// program quits, either from a q/ctrl-c key press in Update or from Quit() in
// Summary/Close; done is closed on that exit so Quit can join it.
func (t *ttyRenderer) start(meta RunMeta) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.prog != nil {
		return
	}
	m := newRunModel(meta, t.cancel)
	t.prog = tea.NewProgram(m, tea.WithOutput(t.w))
	t.done = make(chan struct{})
	go func() {
		defer close(t.done)
		_, err := t.prog.Run()
		// The program has exited (q/ctrl-c, or an early write/terminal
		// failure). Clear prog so Hooks' send() stops forwarding into a dead
		// program, and record the error so Close can surface it to the caller.
		t.mu.Lock()
		t.runErr = err
		t.prog = nil
		t.mu.Unlock()
	}()
}

// Plan delegates to the plain renderer; the plan prints during the check phase,
// before the live region starts.
func (t *ttyRenderer) Plan(wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string, seeded map[workflow.TaskID]bool) error {
	return t.plain.Plan(wf, resolved, cli, seeded)
}

func (t *ttyRenderer) Warn(msg string) error { return t.plain.Warn(msg) }

// Hooks forwards executor callbacks into the live program as messages. When the
// program has not started, sends are dropped (Header starts it first in normal
// flow).
func (t *ttyRenderer) Hooks() executor.Hooks {
	send := func(msg tea.Msg) {
		t.mu.Lock()
		prog := t.prog
		t.mu.Unlock()
		if prog != nil {
			prog.Send(msg)
		}
	}
	return executor.Hooks{
		OnStart: func(task workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			send(taskStartMsg{id: task.ID, rt: rt, model: m, effort: e, shell: task.IsShell()})
		},
		OnFinish: func(task workflow.Task, res executor.TaskResult, err error) {
			send(taskFinishMsg{id: task.ID, res: res, err: err, shell: task.IsShell()})
		},
	}
}

// Summary stops the live program, then prints the final summary card below the
// committed scrollback.
func (t *ttyRenderer) Summary(wf *workflow.Workflow, rep *executor.Report, expected int) error {
	t.stop()
	return t.plain.Summary(wf, rep, expected)
}

// Close stops the live program if it is still running and surfaces any error
// the program returned (e.g. an early write or terminal failure).
func (t *ttyRenderer) Close() error {
	t.stop()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runErr
}

// stop quits the program and joins its goroutine, so no live goroutine outlives
// the renderer.
func (t *ttyRenderer) stop() {
	t.mu.Lock()
	prog, done := t.prog, t.done
	t.prog = nil
	t.mu.Unlock()
	if prog == nil {
		return
	}
	prog.Quit()
	<-done
}
