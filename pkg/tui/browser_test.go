package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mcuste/loom/pkg/store"
)

// key builds a KeyMsg for a single rune (j, k, /, ...).
func key(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// send folds one message into the model and returns it re-typed, so tests can
// chain navigation without re-asserting the cast each step.
func send(t *testing.T, m *browserModel, msg tea.Msg) *browserModel {
	t.Helper()
	next, _ := m.Update(msg)
	bm, ok := next.(*browserModel)
	if !ok {
		t.Fatalf("Update returned %T, want *browserModel", next)
	}
	return bm
}

// twoRunModel returns a browser over a failed deploy run (with a written
// record on disk so openSelected can Load it) and an ok nightly run.
func twoRunModel(t *testing.T) (*browserModel, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "runs", "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := `{
		"run_id":"20260623T100000Z-a1b2c3","workflow_id":"deploy",
		"started_at":"2026-06-23T10:00:00Z","elapsed_ms":72000,
		"status":"failed","error":"build broke","task_count":2,
		"tasks":[
			{"id":"plan","model":"sonnet","status":"ok","elapsed_ms":4000,"prompt":"Plan it","output":"step one"},
			{"id":"build","model":"sonnet","status":"failed","error":"missing module","elapsed_ms":9000,"prompt":"Build it","output":"error: missing module x"}
		]}`
	if err := os.WriteFile(filepath.Join(dir, "20260623T100000Z-a1b2c3.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	nightly := filepath.Join(root, "runs", "nightly")
	if err := os.MkdirAll(nightly, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nightly, "20260622T030000Z-ff0099.json"),
		[]byte(`{"run_id":"20260622T030000Z-ff0099","workflow_id":"nightly","started_at":"2026-06-22T03:00:00Z","status":"ok","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	headers, err := store.ListAllRuns(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newBrowserModel(&bytes.Buffer{}, headers)
	m = send(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, root
}

// TestBrowserListShowsRuns asserts both runs render in the index, newest first.
func TestBrowserListShowsRuns(t *testing.T) {
	m, _ := twoRunModel(t)
	out := m.View()
	if !strings.Contains(out, "deploy") || !strings.Contains(out, "nightly") {
		t.Fatalf("list missing a workflow:\n%s", out)
	}
	// deploy started later than nightly, so it is the cursor row at the top.
	if m.current().WorkflowID != "deploy" {
		t.Errorf("newest-first cursor: got %q", m.current().WorkflowID)
	}
}

// TestBrowserFilter narrows the index to matching workflows live.
func TestBrowserFilter(t *testing.T) {
	m, _ := twoRunModel(t)
	m = send(t, m, key('/'))
	for _, r := range "night" {
		m = send(t, m, key(r))
	}
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.view) != 1 || m.current().WorkflowID != "nightly" {
		t.Fatalf("filter did not narrow to nightly: view=%d", len(m.view))
	}
	// esc clears the filter and restores the full index.
	m = send(t, m, key('/'))
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.view) != 2 {
		t.Fatalf("esc did not clear filter: view=%d", len(m.view))
	}
}

// TestBrowserOpenAndScroll opens a run, asserts task ids and output render,
// switches focus, and confirms the resume hint surfaces.
func TestBrowserOpenAndScroll(t *testing.T) {
	m, _ := twoRunModel(t)
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // open deploy
	if m.mode != modeDetail || m.rec == nil {
		t.Fatalf("enter did not open detail (recErr=%v)", m.recErr)
	}
	out := m.View()
	if !strings.Contains(out, "plan") || !strings.Contains(out, "build") {
		t.Fatalf("detail missing task ids:\n%s", out)
	}
	if !strings.Contains(out, "step one") {
		t.Fatalf("detail missing selected task output:\n%s", out)
	}

	// j steps to the build task; its output should now show.
	m = send(t, m, key('j'))
	if got := m.tsel; got != 1 {
		t.Fatalf("j did not advance the task cursor: %d", got)
	}
	if !strings.Contains(m.View(), "missing module x") {
		t.Fatalf("build output not shown after stepping task:\n%s", m.View())
	}
	// k steps back to the first task.
	m = send(t, m, key('k'))
	if got := m.tsel; got != 0 {
		t.Fatalf("k did not step back: %d", got)
	}

	// Resume hint references the concrete run id.
	m = send(t, m, key('r'))
	if !strings.Contains(m.View(), "loom resume 20260623T100000Z-a1b2c3") {
		t.Fatalf("resume hint missing:\n%s", m.footer(""))
	}

	// esc returns to the list.
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeList {
		t.Fatalf("esc did not return to list")
	}
}

// isQuit reports whether cmd is (or batches) bubbletea's quit command, by
// invoking it and inspecting the resulting message.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestBrowserQuit asserts q quits from the list and from an opened run, so the
// program never strands a user without an exit.
func TestBrowserQuit(t *testing.T) {
	m, _ := twoRunModel(t)
	_, cmd := m.Update(key('q'))
	if !isQuit(cmd) || !m.quit {
		t.Fatalf("q did not quit the list view")
	}

	m, _ = twoRunModel(t)
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // open detail
	_, cmd = m.Update(key('q'))
	if !isQuit(cmd) || !m.quit {
		t.Fatalf("q did not quit the detail view")
	}
}

// longOutputModel returns a detail-mode model whose single task has an output
// taller than any pane, so scrolling has somewhere to go.
func longOutputModel(t *testing.T) *browserModel {
	t.Helper()
	var sb strings.Builder
	for i := range 200 {
		fmt.Fprintf(&sb, "output line %d\n", i)
	}
	rec := &store.RunRecord{
		RunID: "20260623T100000Z-a1b2c3", WorkflowID: "deploy", Status: "ok",
		Tasks: []store.TaskRecord{{ID: "big", Status: "ok", Prompt: "p", Output: sb.String()}},
	}
	m := newBrowserModel(&bytes.Buffer{}, nil)
	m.mode, m.rec = modeDetail, rec
	m.buildSteps()
	return send(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
}

// TestBrowserOutputScrolls verifies the output pane scrolls via the page keys
// and the mouse wheel (j/k are reserved for task navigation), and that g/G
// jump to the extremes, on content taller than the pane.
func TestBrowserOutputScrolls(t *testing.T) {
	m := longOutputModel(t)
	if m.oscroll != 0 {
		t.Fatalf("fresh detail should start at top, got %d", m.oscroll)
	}
	// j is task navigation, not scroll: with a single task it must not scroll.
	m = send(t, m, key('j'))
	if m.oscroll != 0 {
		t.Fatalf("j must not scroll the output (it steps tasks), got %d", m.oscroll)
	}
	m = send(t, m, key('J'))
	_ = m.View()
	if m.oscroll != 10 {
		t.Fatalf("J should page output by ten, got %d", m.oscroll)
	}
	// Mouse wheel scrolls three lines per notch.
	m = send(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	_ = m.View()
	if m.oscroll != 13 {
		t.Fatalf("wheel down should add three, got %d", m.oscroll)
	}
	m = send(t, m, tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if m.oscroll != 10 {
		t.Fatalf("wheel up should subtract three, got %d", m.oscroll)
	}
	m = send(t, m, key('G'))
	_ = m.View() // clamps the scroll-to-bottom sentinel to the last page
	bottom := m.oscroll
	if bottom == 0 || bottom >= len(m.detailLines(60)) {
		t.Fatalf("G did not settle on a valid last page: %d", bottom)
	}
	m = send(t, m, key('g'))
	if m.oscroll != 0 {
		t.Fatalf("g should jump to top, got %d", m.oscroll)
	}
}

// waveTreeManifest is a diamond DAG: setup, then build_a and build_b in
// parallel, then ship. It parses against the registered claude-code runtime.
const waveTreeManifest = `
name: pipe
runtime: claude-code
model: opus
tasks:
  - id: setup
    prompt: do setup
  - id: build_a
    prompt: build a
    depends_on: [setup]
  - id: build_b
    prompt: build b
    depends_on: [setup]
  - id: ship
    prompt: ship it
    depends_on: [build_a, build_b]
`

// waveTreeModel opens a run of waveTreeManifest where setup and build_a
// succeeded, build_b failed, and ship never ran.
func waveTreeModel(t *testing.T) *browserModel {
	t.Helper()
	rec := &store.RunRecord{
		RunID: "20260623T100000Z-a1b2c3", WorkflowID: "pipe", Status: "failed",
		Manifest: waveTreeManifest,
		Tasks: []store.TaskRecord{
			{ID: "setup", Status: "ok", ElapsedMs: 4000, Output: "ready"},
			{ID: "build_a", Status: "ok", ElapsedMs: 9000, Output: "artifact a"},
			{ID: "build_b", Status: "failed", Error: "compile error", ElapsedMs: 2000, Output: "boom"},
		},
	}
	m := newBrowserModel(&bytes.Buffer{}, nil)
	m.mode, m.rec = modeDetail, rec
	m.buildSteps()
	return send(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
}

// TestBrowserWaveTree asserts the step pane groups tasks into execution waves
// (so parallelism is visible) and that a never-run task still appears.
func TestBrowserWaveTree(t *testing.T) {
	m := waveTreeModel(t)
	if m.wf == nil {
		t.Fatalf("manifest did not parse; wave tree falls back to flat list")
	}
	if len(m.steps) != 4 {
		t.Fatalf("want 4 steps, got %d", len(m.steps))
	}
	waves := []int{m.steps[0].wave, m.steps[1].wave, m.steps[2].wave, m.steps[3].wave}
	if waves[0] != 0 || waves[1] != 1 || waves[2] != 1 || waves[3] != 2 {
		t.Fatalf("unexpected wave assignment: %v", waves)
	}
	if m.waveSize(1) != 2 {
		t.Fatalf("wave 2 should hold the two parallel builds, got %d", m.waveSize(1))
	}

	out := m.View()
	for _, want := range []string{"wave 1", "wave 2", "wave 3", m.sym.par, "ship"} {
		if !strings.Contains(out, want) {
			t.Fatalf("step pane missing %q:\n%s", want, out)
		}
	}
}

// TestBrowserWaveTreeNeverRan checks that selecting the gated-out task surfaces
// that it did not run, its dependencies, and the manifest prompt template.
func TestBrowserWaveTreeNeverRan(t *testing.T) {
	m := waveTreeModel(t)
	// ship is the last step and has no record.
	m.tsel = len(m.steps) - 1
	if m.steps[m.tsel].id != "ship" || m.steps[m.tsel].rec != nil {
		t.Fatalf("expected ship with no record at the cursor")
	}
	lines := strings.Join(m.detailLines(60), "\n")
	if !strings.Contains(lines, "did not run") {
		t.Fatalf("never-run step should say so:\n%s", lines)
	}
	if !strings.Contains(lines, "build_a") || !strings.Contains(lines, "build_b") {
		t.Fatalf("never-run step should list its deps:\n%s", lines)
	}
	if !strings.Contains(lines, "ship it") {
		t.Fatalf("never-run step should show the manifest prompt template:\n%s", lines)
	}
}

// TestBrowserOutputScrollClamps ensures the output scroll offset never runs
// past the content even under aggressive paging.
func TestBrowserOutputScrollClamps(t *testing.T) {
	m := longOutputModel(t)
	for range 200 {
		m = send(t, m, key('J'))
	}
	_ = m.View() // View clamps oscroll against content height
	lines := m.detailLines(60)
	if m.oscroll >= len(lines) {
		t.Fatalf("oscroll %d ran past content %d", m.oscroll, len(lines))
	}
}
