package tui_test

import (
	"bytes"
	"strings"
	"testing"

	_ "github.com/mcuste/loom/pkg/runtime/claudecode" // register the claude-code runtime
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// loopPlanFixture is a workflow with one scoped loop. A top-level `seed` task
// feeds a two-member loop {drain, refine}: drain depends on seed (an entry
// edge) and on the prior iteration's refine (via {{prev.refine}}); refine
// depends on drain (an internal edge). The loop converges by until_empty on
// drain. It exercises the loop-group rendering: id, convergence target, max,
// and body tasks with their deps.
const loopPlanFixture = `
name: demo_loops
description: Loop demo
runtime: claude-code
model: opus
effort: high
tasks:
  - id: seed
    prompt: seed it
loops:
  - id: work
    until_empty: drain
    max: 4
    tasks:
      - id: drain
        depends_on: [seed]
        prompt: drain {{seed}} {{prev.refine}}
      - id: refine
        depends_on: [drain]
        prompt: refine {{drain}}
`

// untilLoopPlanFixture is the same shape as loopPlanFixture but converges via an
// `until` expression over a member output rather than until_empty, so the plan
// rendering must surface the raw convergence expression.
const untilLoopPlanFixture = `
name: demo_until
description: Until-loop demo
runtime: claude-code
model: opus
effort: high
tasks:
  - id: seed
    prompt: seed it
loops:
  - id: polish
    until: '{{drain}} == "done"'
    max: 3
    tasks:
      - id: drain
        depends_on: [seed]
        prompt: drain {{seed}}
      - id: refine
        depends_on: [drain]
        prompt: refine {{drain}}
`

// parsePlan parses src or fails the test; it centralizes the Parse boilerplate
// shared by the loop-rendering scenarios.
func parsePlan(t *testing.T, src string) *workflow.Workflow {
	t.Helper()
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return wf
}

// loopGroupLine returns the single rendered line that labels the loop with the
// given id (the "Loop <id>" group header), failing the test if none exists.
// Anchoring assertions to this line keeps them honest: a bare substring search
// for the loop id would also match the "Workflow" header at the top of the
// output, so any task appearing anywhere would pass vacuously.
func loopGroupLine(t *testing.T, got, id string) string {
	t.Helper()
	want := "Loop " + id
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("loop group label %q not found in:\n%s", want, got)
	return ""
}

// TestRenderPlan_RichNamesLoopAndConvergenceTarget is the required snapshot: with
// a forced color profile, the rich plan for a workflow with a scoped loop must
// name the loop id and its until_empty convergence target so the loop is visible
// without running.
func TestRenderPlan_RichNamesLoopAndConvergenceTarget(t *testing.T) {
	forceASCIIProfile(t)
	wf := parsePlan(t, loopPlanFixture)

	got := tui.RenderPlan(wf, nil, nil, true)
	// Both must co-appear on the loop's group label line; asserting them
	// independently would pass even if they landed in unrelated sections.
	line := loopGroupLine(t, got, "work")
	for _, want := range []string{"work", "until_empty=drain"} {
		if !strings.Contains(line, want) {
			t.Errorf("rich loop group label missing %q in %q\nfull:\n%s", want, line, got)
		}
	}
}

// TestRenderPlan_RichShowsLoopMax pins that the rich plan surfaces the loop's
// iteration cap (max) in its labeled group.
func TestRenderPlan_RichShowsLoopMax(t *testing.T) {
	forceASCIIProfile(t)
	wf := parsePlan(t, loopPlanFixture)

	got := tui.RenderPlan(wf, nil, nil, true)
	if !strings.Contains(got, "max=4") {
		t.Errorf("rich loop group missing %q in:\n%s", "max=4", got)
	}
}

// TestRenderPlan_RichAttributesBodyTasksToLoop pins that the loop's body tasks
// are attributed to the loop: both members appear after the loop's group label,
// so a reader can tell which tasks belong to the loop.
func TestRenderPlan_RichAttributesBodyTasksToLoop(t *testing.T) {
	forceASCIIProfile(t)
	wf := parsePlan(t, loopPlanFixture)

	got := tui.RenderPlan(wf, nil, nil, true)
	// Slice from the loop group label, not a bare "work" (which matches the
	// "Workflow" header at offset 0 and makes the attribution claim vacuous).
	idx := strings.Index(got, "Loop work")
	if idx < 0 {
		t.Fatalf("loop group label \"Loop work\" not found in:\n%s", got)
	}
	body := got[idx:]
	for _, member := range []string{"drain", "refine"} {
		if !strings.Contains(body, member) {
			t.Errorf("loop body task %q not attributed under loop group in:\n%s", member, got)
		}
	}
}

// TestRenderPlan_RichShowsBodyTaskDeps pins that a loop body task lists its
// declared deps in the rendering, so the execution shape inside the loop is
// visible without running.
func TestRenderPlan_RichShowsBodyTaskDeps(t *testing.T) {
	forceASCIIProfile(t)
	wf := parsePlan(t, loopPlanFixture)

	got := tui.RenderPlan(wf, nil, nil, true)
	idx := strings.Index(got, "Loop work")
	if idx < 0 {
		t.Fatalf("loop group label \"Loop work\" not found in:\n%s", got)
	}
	// The deps row must appear inside the loop group (after its label), not just
	// anywhere in a flat wave listing, so the in-loop shape is visible.
	if !strings.Contains(got[idx:], "deps=drain") {
		t.Errorf("loop body task refine missing deps=drain under loop group in:\n%s", got)
	}
}

// TestRenderPlan_RichShowsUntilConvergenceExpression pins the until convergence
// path: a loop converging by expression surfaces its raw until target in the
// rendering rather than an until_empty task name.
func TestRenderPlan_RichShowsUntilConvergenceExpression(t *testing.T) {
	forceASCIIProfile(t)
	wf := parsePlan(t, untilLoopPlanFixture)

	got := tui.RenderPlan(wf, nil, nil, true)
	line := loopGroupLine(t, got, "polish")
	for _, want := range []string{"polish", "until="} {
		if !strings.Contains(line, want) {
			t.Errorf("rich until-loop group label missing %q in %q\nfull:\n%s", want, line, got)
		}
	}
}

// TestRenderPlan_PlainNamesLoopAndConvergenceTarget pins that the plain
// (scripted/piped) renderer also shows the scoped loop as a labeled group naming
// its id and convergence target, so check output is informative without a TTY.
func TestRenderPlan_PlainNamesLoopAndConvergenceTarget(t *testing.T) {
	t.Parallel()
	wf := parsePlan(t, loopPlanFixture)

	var buf bytes.Buffer
	if err := tui.New(&buf).Plan(wf, nil, nil, nil); err != nil {
		t.Fatalf("plain Plan: %v", err)
	}
	got := buf.String()
	line := loopGroupLine(t, got, "work")
	for _, want := range []string{"work", "until_empty=drain"} {
		if !strings.Contains(line, want) {
			t.Errorf("plain loop group label missing %q in %q\nfull:\n%s", want, line, got)
		}
	}
}

// TestRenderPlan_PlainShowsLoopBodyTasks pins that the plain renderer lists the
// loop's body tasks under its group, attributing them to the loop.
func TestRenderPlan_PlainShowsLoopBodyTasks(t *testing.T) {
	t.Parallel()
	wf := parsePlan(t, loopPlanFixture)

	var buf bytes.Buffer
	if err := tui.New(&buf).Plan(wf, nil, nil, nil); err != nil {
		t.Fatalf("plain Plan: %v", err)
	}
	got := buf.String()
	idx := strings.Index(got, "Loop work")
	if idx < 0 {
		t.Fatalf("loop group label \"Loop work\" not found in:\n%s", got)
	}
	body := got[idx:]
	for _, member := range []string{"drain", "refine"} {
		if !strings.Contains(body, member) {
			t.Errorf("plain loop body task %q not attributed under loop group in:\n%s", member, got)
		}
	}
}
