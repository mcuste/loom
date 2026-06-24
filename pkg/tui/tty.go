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
	"github.com/muesli/termenv"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// taskStartMsg and taskFinishMsg are the bubbletea messages the ttyRenderer's
// Hooks forward into the running program via Program.Send. OnStart maps to a
// taskStartMsg, OnFinish to a taskFinishMsg; the live model consumes them to
// move a task between its pending/running/done/failed buckets.
type taskStartMsg struct {
	id       workflow.TaskID
	iter     int
	rt       runtime.Name
	model    runtime.Model
	effort   runtime.Effort
	shell    bool
	sub      bool
	ref      string
	retrying bool
}

type taskFinishMsg struct {
	id    workflow.TaskID
	iter  int
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
	id       workflow.TaskID
	iter     int
	rt       runtime.Name
	model    runtime.Model
	effort   runtime.Effort
	shell    bool
	sub      bool
	ref      string
	state    taskState
	retrying bool
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

	order []workflow.TaskID // running-task render order
	tasks map[workflow.TaskID]*liveTask

	sym     symbolSet         // badge/gauge glyphs for this terminal's profile
	maxCost float64           // workflow budget ceiling (0 when wf.Budget is nil)
	seeded  []workflow.TaskID // resume-seeded tasks, in plan order

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
		sym:     symbolsFor(termenv.TrueColor),
		started: time.Now(),
		width:   80,
		height:  24,
	}
}

// Init starts the spinner tick and commits a seed badge line to scrollback for
// each resume-seeded task. Seeded tasks fire no executor hooks, so the live
// view would otherwise never surface them; emitting their badges up front keeps
// a resume's skipped steps visible.
func (m *runModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick}
	for _, id := range m.seeded {
		cmds = append(cmds, tea.Println(fmt.Sprintf("  %s %s", renderBadge(badgeState{seeded: true}, m.sym), id)))
	}
	return tea.Batch(cmds...)
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
		t.retrying = msg.retrying
		return
	}
	m.order = append(m.order, msg.id)
	m.tasks[msg.id] = &liveTask{
		id:       msg.id,
		iter:     msg.iter,
		rt:       msg.rt,
		model:    msg.model,
		effort:   msg.effort,
		shell:    msg.shell,
		sub:      msg.sub,
		ref:      msg.ref,
		state:    stateRunning,
		retrying: msg.retrying,
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
	return finishLine(msg, m.sym)
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
		badge := ""
		if t.retrying {
			badge = renderBadge(badgeState{retrying: true}, m.sym) + " "
		}
		lines = append(lines, fmt.Sprintf("%s %s %s%s%s", m.spinner.View(), t.id, badge, descriptor(t), iterSuffix(t.iter)))
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
// done/total, running count, accrued cost, and elapsed wall time. A budget
// gauge (spend over MaxCostUSD) is appended when the run carries that metadata.
func (m *runModel) statusBar() string {
	elapsed := time.Since(m.started).Round(time.Second)
	bar := fmt.Sprintf("%d/%d done · %d running · $%.6f · %s",
		m.done, m.total, m.running, m.usage.TotalCostUSD, elapsed)
	if g := budgetGauge(m.usage.TotalCostUSD, m.maxCost, m.sym); g != "" {
		bar += " · " + g
	}
	return bar
}

// descriptor renders the runtime facts shown after a running task's id.
func descriptor(t *liveTask) string {
	switch {
	case t.shell:
		return "(shell)"
	case t.sub:
		return fmt.Sprintf("(subworkflow %s)", t.ref)
	default:
		return fmt.Sprintf("(%s/%s%s)", t.rt, t.model, effortSuffix(t.effort))
	}
}

// finishLine builds the permanent scrollback summary for a finished task,
// mirroring the plain renderer's per-task finish line so scrollback reads the
// same whether or not the live view is active. A leading state badge (drawn
// with glyph set sym) distinguishes a failure, a cache hit, and a when-skip
// from a plain completion; a failure shows the trimmed error inline before the
// summary columns.
func finishLine(msg taskFinishMsg, sym symbolSet) string {
	id := msg.id
	elapsed := msg.res.Elapsed.Round(time.Millisecond)
	var line string
	switch {
	case msg.err != nil:
		line = fmt.Sprintf("  %s %s FAIL after %s: %s", sym.failed, id, elapsed, strings.TrimSpace(msg.err.Error()))
	case msg.shell:
		line = fmt.Sprintf("  %s %s done %s  exit=0", sym.done, id, elapsed)
	case msg.res.Status == executor.StatusSkipped:
		line = fmt.Sprintf("  %s %s %s", renderBadge(badgeState{res: msg.res}, sym), id, elapsed)
	case msg.res.CacheHit:
		line = fmt.Sprintf("  %s %s %s  $%.6f", renderBadge(badgeState{res: msg.res}, sym), id, elapsed, msg.res.Usage.TotalCostUSD)
	default:
		u := msg.res.Usage
		line = fmt.Sprintf("  %s %s done %s  in=%d out=%d cache=%d  $%.6f",
			sym.done, id, elapsed, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.TotalCostUSD)
	}
	return line + iterSuffix(msg.iter)
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

	// seeded and maxCost are captured from Plan (which runs before the live
	// program starts) and threaded into the model in start: seeded drives the
	// resume seed badges, maxCost the status-bar budget gauge.
	seeded  []workflow.TaskID
	maxCost float64

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
	m.sym = symbolsFor(termenv.NewOutput(t.w).Profile)
	m.maxCost = t.maxCost
	m.seeded = t.seeded
	prog := tea.NewProgram(m, tea.WithOutput(t.w))
	t.prog = prog
	t.done = make(chan struct{})
	go func() {
		defer close(t.done)
		// Use the captured prog, not t.prog: stop() clears t.prog under t.mu
		// while this goroutine runs, so reading the field here would race.
		_, err := prog.Run()
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
	// Capture the seeded set in plan order and the budget ceiling here, before
	// the live program starts: the live view learns about seeded tasks (which
	// fire no executor hooks) and the budget gauge ceiling only through this
	// call.
	t.seeded = nil
	for _, id := range wf.Plan() {
		if seeded[id] {
			t.seeded = append(t.seeded, id)
		}
	}
	if wf.Budget != nil {
		t.maxCost = wf.Budget.MaxCostUSD
	}
	return t.plain.Plan(wf, resolved, cli, seeded)
}

// Warn delegates to the plain renderer. Warnings print during the check phase,
// before the live region starts, so the plain writer handles them directly.
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
		// iter is forwarded into the messages so the live model can badge a
		// looped task's running and finish lines with its pass number; iter is
		// 0 for a non-looped task, where the badge is empty.
		OnStart: func(task workflow.Task, iter int, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			send(taskStartMsg{id: task.ID, iter: iter, rt: rt, model: m, effort: e, shell: task.IsShell(), sub: task.IsSubWorkflow(), ref: task.Workflow})
		},
		OnFinish: func(task workflow.Task, iter int, res executor.TaskResult, err error) {
			send(taskFinishMsg{id: task.ID, iter: iter, res: res, err: err, shell: task.IsShell()})
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
