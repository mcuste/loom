package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// runLoader abstracts loading a run record from disk so the browser model
// can be tested without a real filesystem.
type runLoader interface {
	Load(path string) (*store.RunRecord, error)
}

// runLoaderFunc adapts a function to runLoader.
type runLoaderFunc func(string) (*store.RunRecord, error)

func (f runLoaderFunc) Load(path string) (*store.RunRecord, error) { return f(path) }

// Browse runs the interactive run browser over headers until the user quits.
// It takes over the screen (alternate buffer) and reads keys from the default
// input; callers should invoke it only for an interactive terminal (Rich(w)),
// falling back to RunsTable otherwise. Run detail is read from disk lazily via
// store.Load when a run is opened, so the index stays cheap for large
// histories. An empty header slice still opens (showing an empty index); the
// caller may prefer RunsTable in that case.
func Browse(w io.Writer, headers []store.RunHeader) error {
	m := newBrowserModel(w, headers)
	// Cell-motion mode delivers wheel events so the detail pane scrolls with the
	// mouse. It does capture the mouse (the terminal's native text selection is
	// suspended while the browser runs), an accepted trade for wheel scrolling.
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(w))
	_, err := prog.Run()
	return err
}

// browserMode is the browser's top-level screen: the runs index or one opened
// run's detail.
type browserMode int

const (
	modeList browserMode = iota
	modeDetail
)

// browserModel is the bubbletea model for `loom runs`. It holds the run index
// (all), the filtered view into it (view), and, once a run is opened, the
// lazily loaded record plus the detail-pane cursor and scroll state.
type browserModel struct {
	all  []store.RunHeader
	view []int // indices into all that match the active filter, newest first
	sel  int   // cursor within view
	top  int   // first visible view row (list scroll offset)

	filter    string
	filtering bool // true while the / filter input is open

	mode browserMode

	// Detail-screen state, valid only in modeDetail.
	rec     *store.RunRecord
	recErr  error
	wf      *workflow.Workflow // parsed from rec.Manifest; nil if it won't parse
	steps   []stepNode         // run's tasks in wave order; tsel indexes this
	tsel    int                // step cursor within steps
	ttop    int                // first visible step-pane row (incl. wave headers)
	oscroll int                // output-pane scroll offset

	loader runLoader

	sym    symbolSet
	width  int
	height int
	status string // transient one-line message (resume hint, errors)
	quit   bool
}

func newBrowserModel(w io.Writer, headers []store.RunHeader) *browserModel {
	m := &browserModel{
		all:    headers,
		loader: runLoaderFunc(store.Load),
		sym:    symbolsFor(termenv.NewOutput(w).Profile),
		width:  80,
		height: 24,
	}
	m.recomputeView()
	return m
}

func (m *browserModel) Init() tea.Cmd { return nil }

// recomputeView rebuilds the filtered index from the active filter and clamps
// the cursor into the new range. A blank filter matches every run.
func (m *browserModel) recomputeView() {
	m.view = m.view[:0]
	q := strings.ToLower(strings.TrimSpace(m.filter))
	for i, h := range m.all {
		if q == "" || headerMatches(h, q) {
			m.view = append(m.view, i)
		}
	}
	if m.sel >= len(m.view) {
		m.sel = max(0, len(m.view)-1)
	}
	m.top = 0
}

// headerMatches reports whether h matches the lowercased filter query as a
// substring of its workflow id, status, run id, or short id.
func headerMatches(h store.RunHeader, q string) bool {
	return strings.Contains(strings.ToLower(h.WorkflowID), q) ||
		strings.Contains(strings.ToLower(h.Status), q) ||
		strings.Contains(strings.ToLower(h.RunID), q)
}

// current returns the header under the list cursor, or nil when the view is
// empty.
func (m *browserModel) current() *store.RunHeader {
	if len(m.view) == 0 {
		return nil
	}
	return &m.all[m.view[m.sel]]
}

func (m *browserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.MouseMsg:
		// The wheel scrolls the detail pane's output; clicks and motion are
		// ignored. Three lines per notch matches a typical terminal pager.
		if m.mode == modeDetail && !m.filtering {
			switch msg.Button {
			case tea.MouseButtonWheelDown:
				m.oscroll += 3
			case tea.MouseButtonWheelUp:
				m.oscroll = max(0, m.oscroll-3)
			}
		}
		return m, nil
	case tea.KeyMsg:
		if m.filtering {
			return m.updateFilter(msg)
		}
		if m.mode == modeDetail {
			return m.updateDetail(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

// updateFilter folds key presses while the / filter input is open: enter and
// esc both close the input (enter keeps the query, esc clears it), backspace
// deletes, and any rune extends the query, re-filtering live.
func (m *browserModel) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.filtering = false
	case "esc":
		m.filtering = false
		m.filter = ""
		m.recomputeView()
	case "backspace":
		if m.filter != "" {
			r := []rune(m.filter)
			m.filter = string(r[:len(r)-1])
			m.recomputeView()
		}
	default:
		if len(msg.Runes) > 0 {
			m.filter += string(msg.Runes)
			m.recomputeView()
		}
	}
	return m, nil
}

// updateList handles navigation on the runs index.
func (m *browserModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.status = ""
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		m.quit = true
		return m, tea.Quit
	case "j", "down":
		if m.sel < len(m.view)-1 {
			m.sel++
		}
	case "k", "up":
		if m.sel > 0 {
			m.sel--
		}
	case "g", "home":
		m.sel = 0
	case "G", "end":
		m.sel = max(0, len(m.view)-1)
	case "/":
		m.filtering = true
	case "enter", "l", "right":
		if m.current() != nil {
			m.openSelected()
		}
	}
	return m, nil
}

// openSelected loads the run under the cursor into detail state and switches to
// the detail screen. A load error is held in recErr and surfaced in the detail
// pane rather than aborting the browser.
func (m *browserModel) openSelected() {
	h := m.current()
	m.rec, m.recErr = m.loader.Load(h.Path)
	m.mode = modeDetail
	m.tsel, m.ttop, m.oscroll = 0, 0, 0
	m.status = ""
	if m.recErr == nil {
		m.buildSteps()
	}
}

// buildSteps derives the step nodes for the opened run by delegating to
// NewRunView, which encapsulates the manifest-parsing and wave-grouping logic
// shared with the plain renderers. The browser model retains only the cursor,
// scroll, and filter state that is inherently interactive.
func (m *browserModel) buildSteps() {
	rv := NewRunView(m.rec)
	m.wf = rv.Wf
	m.steps = rv.Steps
}

// scrollBottom is the sentinel offset for "scroll to the end": View clamps it
// down to the real last page once it knows the content and pane heights.
const scrollBottom = 1 << 30

// updateDetail handles navigation within an opened run. j/k and the arrows
// (plus tab/shift+tab) move between the run's tasks, selecting which task's
// output the right pane shows; the output itself scrolls with the mouse wheel,
// PgUp/PgDn, ctrl+u/ctrl+d, J/K, and space. Switching tasks resets the scroll.
func (m *browserModel) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "esc", "backspace":
		m.mode = modeList
		m.status = ""
	case "j", "down", "tab", "n":
		m.moveTask(1)
	case "k", "up", "shift+tab", "p":
		m.moveTask(-1)
	case "r":
		if m.rec != nil {
			m.status = "resume this run:  loom resume " + m.rec.RunID
		}
	case "J", "pgdown", " ", "ctrl+d", "f":
		m.oscroll += 10
	case "K", "pgup", "ctrl+u", "b":
		m.oscroll = max(0, m.oscroll-10)
	case "ctrl+e":
		m.oscroll++
	case "ctrl+y":
		if m.oscroll > 0 {
			m.oscroll--
		}
	case "g":
		m.oscroll = 0
	case "G":
		m.oscroll = scrollBottom
	}
	return m, nil
}

// moveTask advances the step cursor by delta within bounds and resets the
// output scroll so a freshly selected step starts at its top.
func (m *browserModel) moveTask(delta int) {
	if len(m.steps) == 0 {
		return
	}
	m.tsel = clamp(m.tsel+delta, 0, len(m.steps)-1)
	m.oscroll = 0
}

func (m *browserModel) View() string {
	if m.quit {
		return ""
	}
	if m.mode == modeDetail {
		return m.viewDetail()
	}
	return m.viewList()
}

// viewList renders the runs index: a title line, a column header, the scrolled
// rows with the cursor highlighted, and a footer (the filter input when open,
// otherwise the keybinding hint).
func (m *browserModel) viewList() string {
	now := time.Now()
	title := browserTitleStyle.Render("loom runs")
	count := dimStyle.Render(fmt.Sprintf("%d run%s", len(m.view), plural(len(m.view))))
	if f := strings.TrimSpace(m.filter); f != "" {
		count = dimStyle.Render(fmt.Sprintf("%d/%d match %q", len(m.view), len(m.all), f))
	}
	head := dimStyle.Render(fmt.Sprintf("  %-1s %-7s %-10s %-18s %-9s %5s %8s  %s",
		"", "STATUS", "WHEN", "WORKFLOW", "RUN", "TASKS", "DURATION", "COST"))

	area := max(1, m.height-3)
	m.clampList(area)

	var rows []string
	if len(m.view) == 0 {
		rows = append(rows, dimStyle.Render("  no runs match"))
	}
	for i := m.top; i < len(m.view) && i < m.top+area; i++ {
		h := m.all[m.view[i]]
		row := fmt.Sprintf("%s %-7s %-10s %-18s %-9s %5d %8s  $%.4f",
			statusGlyph(h.Status, m.sym), trunc(h.Status, 7),
			relTime(h.StartedAt, now), trunc(h.WorkflowID, 18),
			shortID(h.RunID), h.TaskCount, humanDur(h.ElapsedMs), h.TotalCostUSD)
		if i == m.sel {
			rows = append(rows, selectedRowStyle.Width(m.width).Render("▌"+row))
		} else {
			rows = append(rows, " "+row)
		}
	}

	body := strings.Join(rows, "\n")
	footer := m.footer("j/k move · enter open · / filter · g/G top/bottom · q quit")
	return joinScreen(m.height,
		title+"  "+count,
		head,
		body,
		footer)
}

// clampList keeps the list cursor inside the visible window of the given
// height by sliding the top offset.
func (m *browserModel) clampList(area int) {
	if m.sel < m.top {
		m.top = m.sel
	}
	if m.sel >= m.top+area {
		m.top = m.sel - area + 1
	}
}

// viewDetail renders the opened-run screen: a breadcrumb title, then a
// side-by-side task list and task-detail pane, then a footer.
func (m *browserModel) viewDetail() string {
	if m.recErr != nil {
		return joinScreen(m.height,
			browserTitleStyle.Render("loom runs"),
			failStyle.Render("  load error: "+m.recErr.Error()),
			"",
			m.footer("esc back · q quit"))
	}

	bodyH := max(1, m.height-2)
	leftW := clamp(m.width/3, 24, 40)
	rightW := max(20, m.width-leftW-3)

	left := m.taskPane(leftW, bodyH)
	right := m.outputPane(rightW, bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		left,
		paneBorderStyle.Height(bodyH).Render(right))

	footer := m.footer("j/k step · wheel/PgUp·PgDn/⌃d·⌃u scroll · r resume · esc back · q quit")
	return joinScreen(m.height, m.detailTitle(), body, footer)
}

// detailTitle is the breadcrumb header for an opened run.
func (m *browserModel) detailTitle() string {
	r := m.rec
	glyph := coloredStatus(r.Status, m.sym)
	return fmt.Sprintf("%s %s %s %s · %s · %s · $%.4f",
		browserTitleStyle.Render("runs"), dimStyle.Render("›"),
		browserTitleStyle.Render(r.WorkflowID),
		dimStyle.Render(shortID(r.RunID)),
		glyph+" "+r.Status, humanDur(r.ElapsedMs), r.Usage.TotalCostUSD)
}

// paneRow is one line of the step pane: either a wave header (stepIdx < 0, dim)
// or a selectable step (stepIdx indexes m.steps). text is plain (no styling) so
// it truncates and pads by rune count without ANSI corruption; taskPane applies
// the dim or selection style after fitting it to the pane width.
type paneRow struct {
	text    string
	dim     bool
	stepIdx int
}

// taskPane renders the opened run as a vertical dependency tree: tasks are
// grouped into execution waves (a wave's members run in parallel), drawn under
// a "wave N" header with ├─/└─ branch glyphs. The selected step is highlighted
// and the pane scrolls to keep it visible. j/k (and tab) move the selection;
// the right pane shows the selected step's output.
func (m *browserModel) taskPane(width, height int) string {
	title := paneTitleStyle.Render("STEPS")
	rowsH := max(1, height-1)

	rows := m.stepRows(width)
	sel := selectedRowIndex(rows, m.tsel)

	// Keep the selected step's row inside the visible window.
	if sel >= 0 {
		if sel < m.ttop {
			m.ttop = sel
		}
		if sel >= m.ttop+rowsH {
			m.ttop = sel - rowsH + 1
		}
	}
	if m.ttop > max(0, len(rows)-rowsH) {
		m.ttop = max(0, len(rows)-rowsH)
	}

	var out []string
	for i := m.ttop; i < len(rows) && i < m.ttop+rowsH; i++ {
		switch {
		case rows[i].stepIdx == m.tsel:
			out = append(out, selectedRowStyle.Width(width).Render(rows[i].text))
		case rows[i].dim:
			out = append(out, dimStyle.Render(rows[i].text))
		default:
			out = append(out, rows[i].text)
		}
	}
	content := title + "\n" + strings.Join(out, "\n")
	return lipgloss.NewStyle().Width(width).Render(content)
}

// stepRows flattens the wave-grouped steps into plain renderable pane rows: a
// header per wave (omitted in the flat fallback) followed by each step's tree
// line. A step line carries its tree branch, status glyph, id (with a loop ×N
// suffix), and right-aligned duration. Text is kept plain so it fits the pane
// width by rune count; taskPane colors it.
func (m *browserModel) stepRows(width int) []paneRow {
	var rows []paneRow
	lastWave := -2
	for i, s := range m.steps {
		if s.wave >= 0 && s.wave != lastWave {
			lastWave = s.wave
			label := fmt.Sprintf("wave %d", s.wave+1)
			if m.waveSize(s.wave) > 1 {
				label += " " + m.sym.par
			}
			rows = append(rows, paneRow{text: trunc(label, width), dim: true, stepIdx: -1})
		}
		branch := m.sym.tee
		if s.last {
			branch = m.sym.elbow
		}
		glyph := m.sym.pending
		if s.rec != nil {
			glyph = statusGlyph(s.rec.Status, m.sym)
		}
		id := string(s.id)
		if s.iters > 1 {
			id += fmt.Sprintf(" ×%d", s.iters)
		}
		line := trunc(fmt.Sprintf("%s %s %s", branch, glyph, id), width)
		if dur := stepDuration(s); dur != "" {
			if pad := width - len([]rune(line)) - len(dur); pad > 0 {
				line += strings.Repeat(" ", pad) + dur
			}
		}
		rows = append(rows, paneRow{text: line, stepIdx: i})
	}
	return rows
}

// waveSize reports how many steps share a wave, so a wave with concurrency can
// be flagged.
func (m *browserModel) waveSize(wave int) int {
	n := 0
	for _, s := range m.steps {
		if s.wave == wave {
			n++
		}
	}
	return n
}

// stepDuration is the step's elapsed time, blank when it never ran.
func stepDuration(s stepNode) string {
	if s.rec == nil {
		return ""
	}
	return humanDur(s.rec.ElapsedMs)
}

// selectedRowIndex returns the pane-row index that renders step stepIdx, or -1.
func selectedRowIndex(rows []paneRow, stepIdx int) int {
	for i, r := range rows {
		if r.stepIdx == stepIdx {
			return i
		}
	}
	return -1
}

// outputPane renders the selected task's prompt/command, output, and error,
// wrapped to the pane width and windowed by the output scroll offset. The
// scroll offset is clamped here so it can never run past the content.
func (m *browserModel) outputPane(width, height int) string {
	lines := m.detailLines(width)
	rowsH := max(1, height-1)

	maxScroll := max(0, len(lines)-rowsH)
	if m.oscroll > maxScroll {
		m.oscroll = maxScroll
	}

	title := paneTitleStyle.Render("STEP")
	if s := m.selectedStep(); s != nil {
		title = paneTitleStyle.Render("STEP " + string(s.id))
		if len(lines) > rowsH {
			title += dimStyle.Render(fmt.Sprintf("  [%d-%d/%d]",
				m.oscroll+1, min(m.oscroll+rowsH, len(lines)), len(lines)))
		}
	}

	end := min(m.oscroll+rowsH, len(lines))
	window := lines[m.oscroll:end]
	return title + "\n" + strings.Join(window, "\n")
}

// selectedStep returns the step under the cursor, or nil when the run has none.
func (m *browserModel) selectedStep() *stepNode {
	if m.tsel < 0 || m.tsel >= len(m.steps) {
		return nil
	}
	return &m.steps[m.tsel]
}

// detailLines builds the wrapped content lines for the selected step: a status
// header (its dependencies, when known), the prompt or shell command, the
// output, and any error, each under a labeled separator. A step that never ran
// shows why it has no record and falls back to the manifest's prompt template.
func (m *browserModel) detailLines(width int) []string {
	s := m.selectedStep()
	if s == nil {
		return []string{dimStyle.Render("(no steps recorded)")}
	}
	var out []string
	if deps := m.stepDeps(s.id); deps != "" {
		out = append(out, dimStyle.Render("needs: "+deps))
	}

	if s.rec == nil {
		out = append(out, dimStyle.Render("· did not run (gated out or never reached)"))
		if t := m.taskFromManifest(s.id); t != nil {
			body, label := t.Command, "── command (template) ──"
			if body == "" {
				body, label = t.Prompt, "── prompt (template) ──"
			}
			if body != "" {
				out = append(out, "", sectionStyle.Render(label))
				out = append(out, wrapText(body, width)...)
			}
		}
		return out
	}

	tr := s.rec
	out = append(out, fmt.Sprintf("%s %s  %s  $%.6f",
		coloredStatus(tr.Status, m.sym), tr.Status, humanDur(tr.ElapsedMs), tr.Usage.TotalCostUSD))
	if tr.Error != "" {
		out = append(out, failStyle.Render(trunc(tr.Error, width)))
	}
	out = append(out, "")

	if tr.Command != "" {
		out = append(out, sectionStyle.Render("── command ──"))
		out = append(out, wrapText(tr.Command, width)...)
	} else if tr.Prompt != "" {
		out = append(out, sectionStyle.Render("── prompt ──"))
		out = append(out, wrapText(tr.Prompt, width)...)
	} else if t := m.taskFromManifest(s.id); t != nil && t.IsSubWorkflow() {
		out = append(out, sectionStyle.Render("── sub-workflow ──"))
		out = append(out, wrapText("workflow="+t.Workflow, width)...)
	}
	out = append(out, "", sectionStyle.Render("── output ──"))
	if strings.TrimSpace(tr.Output) == "" {
		out = append(out, dimStyle.Render("(no output)"))
	} else {
		out = append(out, wrapText(tr.Output, width)...)
	}
	return out
}

// stepDeps returns the comma-joined dependencies of a task from the parsed
// manifest, or "" when the manifest is absent or the task has none.
func (m *browserModel) stepDeps(id workflow.TaskID) string {
	t := m.taskFromManifest(id)
	if t == nil || len(t.DependsOn) == 0 {
		return ""
	}
	return depsList(t.DependsOn)
}

// taskFromManifest looks up a task in the parsed workflow, or nil when the
// manifest did not parse.
func (m *browserModel) taskFromManifest(id workflow.TaskID) *workflow.Task {
	if m.wf == nil {
		return nil
	}
	return m.wf.ByID(id)
}

// footer renders the bottom line: the active filter input, a transient status
// message when one is set, or the supplied keybinding hint.
func (m *browserModel) footer(hint string) string {
	if m.filtering {
		return "/" + m.filter + dimStyle.Render("▏")
	}
	if m.status != "" {
		return okStyle.Render(m.status)
	}
	return browserHelpStyle.Render(hint)
}

// browser styles render against lipgloss's global color profile, degrading to
// plain text on a profile that lacks color (mirroring render_plan.go).
var (
	browserTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	browserHelpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	selectedRowStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("63"))
	paneTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	paneBorderStyle   = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(lipgloss.Color("240")).PaddingLeft(1)
	dimStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	failStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
)

// coloredStatus returns the status glyph tinted by outcome (green ok, red
// fail, dim otherwise) for use in headers where no padding follows.
func coloredStatus(status string, sym symbolSet) string {
	g := statusGlyph(status, sym)
	switch status {
	case store.StatusOK:
		return okStyle.Render(g)
	case store.StatusFailed:
		return failStyle.Render(g)
	default:
		return dimStyle.Render(g)
	}
}

// joinScreen stacks the given sections into a screen exactly height rows tall:
// the first sections are pinned to the top, the last is pinned to the bottom,
// and the gap between is padded with blank lines. Sections may themselves be
// multi-line. Content taller than the screen is left to scroll naturally.
func joinScreen(height int, sections ...string) string {
	if len(sections) == 0 {
		return ""
	}
	footer := sections[len(sections)-1]
	body := strings.Join(sections[:len(sections)-1], "\n")
	used := lipgloss.Height(body) + lipgloss.Height(footer)
	pad := max(0, height-used)
	return body + strings.Repeat("\n", pad+1) + footer
}

// wrapText hard-wraps s to width columns, preserving existing newlines. It
// breaks on rune boundaries (not words) so long unbroken output and code stay
// within the pane.
func wrapText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		r := []rune(line)
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return out
}

// trunc shortens s to at most width runes, appending an ellipsis when it cuts.
func trunc(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:max(0, width)])
	}
	return string(r[:width-1]) + "…"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
