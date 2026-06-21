package tui_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	_ "github.com/mcuste/loom/pkg/runtime/claudecode" // register the claude-code runtime
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// planFixture is a diamond DAG with a description, workflow-level
// runtime/model/effort, a loop, and three params: one supplied via CLI, one
// falling back to its default, and one required-but-missing. It exercises every
// branch of the rich renderer (header card, param provenance, wave grouping).
const planFixture = `
name: demo
description: Demo workflow
runtime: claude-code
model: opus
effort: high
loop:
  until_empty: d
  max: 5
params:
  - name: target
    required: true
  - name: env
    default: prod
  - name: secret
    required: true
tasks:
  - id: a
    prompt: use {{params.target}} for {{params.env}} {{params.secret}}
  - id: b
    depends_on: [a]
    prompt: x {{a}}
  - id: c
    depends_on: [a]
    prompt: x {{a}}
  - id: d
    depends_on: [b, c]
    prompt: x {{b}} {{c}}
`

// forceASCIIProfile pins lipgloss to the Ascii color profile so the rich
// rendering is deterministic across terminals (no environment-dependent escape
// codes leak into the snapshot).
func forceASCIIProfile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	lipgloss.SetColorProfile(termenv.Ascii)
}

// resolvedFor returns the resolved param bag for the fixture: target supplied
// via CLI, env taken from its default, and no entry for secret (the required
// param left unsupplied), so its row exercises the MISSING provenance branch.
func resolvedFor() (workflow.ParamValues, map[string]string) {
	resolved := workflow.ParamValues{"target": "ship-it", "env": "prod"}
	cli := map[string]string{"target": "ship-it"}
	return resolved, cli
}

// TestRenderPlan_PlainMatchesPlainRenderer pins that the non-rich branch is
// byte-identical to the existing plainRenderer.Plan output, so adding the rich
// path does not disturb scripted/piped check output.
func TestRenderPlan_PlainMatchesPlainRenderer(t *testing.T) {
	wf, err := workflow.Parse([]byte(planFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved, cli := resolvedFor()

	var buf bytes.Buffer
	if err := tui.New(&buf).Plan(wf, resolved, cli, nil); err != nil {
		t.Fatalf("plain Plan: %v", err)
	}

	got := tui.RenderPlan(wf, resolved, cli, false)
	if got != buf.String() {
		t.Errorf("RenderPlan(rich=false) = %q, want plain output %q", got, buf.String())
	}
}

// TestRenderPlan_RichDrawsHeaderCard pins that the rich branch boxes the
// workflow identity card: id, description, runtime/model/effort, and the loop
// marker all appear in the rendering.
func TestRenderPlan_RichDrawsHeaderCard(t *testing.T) {
	forceASCIIProfile(t)
	wf, err := workflow.Parse([]byte(planFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved, cli := resolvedFor()

	got := tui.RenderPlan(wf, resolved, cli, true)
	for _, want := range []string{"demo", "Demo workflow", "claude-code", "opus", "high"} {
		if !strings.Contains(got, want) {
			t.Errorf("rich header missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderPlan_RichTagsParamProvenance pins the param provenance labels: the
// CLI-supplied param reads "cli", the defaulted param reads "default", and the
// required-but-unsupplied param reads "MISSING".
func TestRenderPlan_RichTagsParamProvenance(t *testing.T) {
	forceASCIIProfile(t)
	wf, err := workflow.Parse([]byte(planFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved, cli := resolvedFor()

	got := tui.RenderPlan(wf, resolved, cli, true)
	for _, want := range []string{"cli", "default", "MISSING"} {
		if !strings.Contains(got, want) {
			t.Errorf("rich params missing provenance %q in:\n%s", want, got)
		}
	}
}

// TestRenderPlan_RichGroupsByWave pins that the rich plan is grouped by
// execution wave rather than a flat numbered list: the fixture's diamond yields
// three waves, and the rendering labels them.
func TestRenderPlan_RichGroupsByWave(t *testing.T) {
	forceASCIIProfile(t)
	wf, err := workflow.Parse([]byte(planFixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	resolved, cli := resolvedFor()

	got := tui.RenderPlan(wf, resolved, cli, true)
	// Three waves for a diamond: {a}, {b,c}, {d}. The label scheme is the
	// renderer's to choose, but each wave's ordinal must surface.
	for _, want := range []string{"Wave 1", "Wave 2", "Wave 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("rich plan missing %q in:\n%s", want, got)
		}
	}
	// The join task d must land in Wave 3, after both branches. Match on its
	// unique deps row (deps=b,c) under the Wave 3 header rather than the bare
	// id "d", which also appears in the loop's until_empty=d marker.
	wave3 := got[strings.Index(got, "Wave 3"):]
	if !strings.Contains(wave3, "deps=b,c") {
		t.Errorf("task d (deps=b,c) not grouped under Wave 3 in:\n%s", got)
	}
}
