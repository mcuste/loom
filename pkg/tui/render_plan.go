package tui

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mcuste/loom/pkg/workflow"
)

// RenderPlan produces the static check rendering for wf as a string. When rich
// is false it reproduces the plain renderer's plan output byte-for-byte; when
// rich is true it draws a boxed header card, a params section with provenance
// coloring (cli / default / MISSING), and the execution plan grouped by wave
// with each task's effective runtime/model/effort and incoming deps.
//
// resolved carries the merged param values and cli the names supplied on the
// command line, together driving each param's provenance tag.
func RenderPlan(wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string, rich bool) string {
	if !rich {
		var buf bytes.Buffer
		// The plain branch is the single source of truth for scripted/piped
		// output; reuse it verbatim rather than risk drift.
		_ = (&plainRenderer{w: &buf}).Plan(wf, resolved, cli, nil)
		return buf.String()
	}
	return renderRichPlan(wf, resolved, cli)
}

// These styles render against lipgloss's global color profile (process state,
// set via lipgloss.SetColorProfile). Colors degrade to plain text under the
// Ascii profile, keeping snapshot output deterministic. Callers that need
// deterministic output (tests, snapshots, piped output) MUST pin the profile
// first; otherwise the rendering inherits whatever profile was last set.
var (
	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	waveStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
	cliTag       = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	defaultTag   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	missingTag   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
)

func renderRichPlan(wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string) string {
	var b strings.Builder
	b.WriteString(richHeaderCard(wf))
	b.WriteString("\n")
	if len(wf.Params) > 0 {
		b.WriteString("\n")
		b.WriteString(richParams(wf, resolved, cli))
	}
	b.WriteString("\n")
	b.WriteString(richWaves(wf))
	if len(wf.Loops) > 0 {
		b.WriteString("\n")
		b.WriteString(richLoops(wf))
	}
	return b.String()
}

// richHeaderCard boxes the workflow identity: id, description, and the
// effective runtime/model/effort triple.
func richHeaderCard(wf *workflow.Workflow) string {
	var rows []string
	rows = append(rows, titleStyle.Render(string(wf.ID)))
	if wf.Description != "" {
		rows = append(rows, wf.Description)
	}
	rows = append(rows, fmt.Sprintf("%s %s   %s %s   %s %s",
		labelStyle.Render("runtime"), orDash(string(wf.Runtime)),
		labelStyle.Render("model"), orDash(string(wf.Model)),
		labelStyle.Render("effort"), orDash(string(wf.Effort)),
	))
	if wf.SystemPrompt != "" {
		rows = append(rows, fmt.Sprintf("%s %s", labelStyle.Render("system"), wf.SystemPrompt))
	}
	return cardStyle.Render(strings.Join(rows, "\n"))
}

// richParams renders the params section with a provenance tag per param,
// reusing paramSource so the cli/default/MISSING labels match the plain branch.
func richParams(wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string) string {
	nameWidth := 0
	for _, prm := range wf.Params {
		if n := len(prm.Name); n > nameWidth {
			nameWidth = n
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s %s %s %s\n", sectionStyle.Render(fmt.Sprintf("Params (%d)", len(wf.Params))),
		labelStyle.Render("provenance:"), paramTag("cli"), paramTag("default"), paramTag("missing"))
	for _, prm := range wf.Params {
		value, ok := resolved[prm.Name]
		source := paramSource(prm, cli, ok)
		shown := "<missing>"
		if ok {
			shown = quoteIfNeeded(value)
		}
		fmt.Fprintf(&b, "  %-*s = %-12s %s\n",
			nameWidth, prm.Name, shown, paramTag(source))
	}
	return b.String()
}

func paramTag(source string) string {
	switch source {
	case "cli":
		return cliTag.Render("(cli)")
	case "default":
		return defaultTag.Render("(default)")
	default:
		return missingTag.Render("(MISSING)")
	}
}

// richWaves prints the execution plan grouped by wave: each wave is a labeled
// group, and every task lists its effective runtime/model/effort and incoming
// deps so the real parallelism is visible at a glance.
func richWaves(wf *workflow.Workflow) string {
	waves := wf.Waves()

	// idWidth is just the widest top-level task id; loop members are drawn under
	// their own groups (see richLoops) and never appear in the wave section, so
	// excluding them keeps this column from being padded for ids it never shows.
	idWidth := 0
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		if t.Loop != "" {
			continue
		}
		if n := len(t.ID); n > idWidth {
			idWidth = n
		}
	}

	// Loop members are drawn under their own loop groups (see richLoops), so the
	// wave listing carries only the top-level DAG. Empty waves (every member
	// pulled into a loop) are dropped and the remainder renumbered.
	var topWaves [][]workflow.TaskID
	for _, wave := range waves {
		var top []workflow.TaskID
		for _, id := range wave {
			if t := wf.ByID(id); t != nil && t.Loop == "" {
				top = append(top, id)
			}
		}
		if len(top) > 0 {
			topWaves = append(topWaves, top)
		}
	}

	var b strings.Builder
	b.WriteString(sectionStyle.Render(fmt.Sprintf("Execution plan (%d wave%s)", len(topWaves), plural(len(topWaves)))))
	b.WriteString("\n")
	for i, wave := range topWaves {
		b.WriteString(waveStyle.Render(fmt.Sprintf("  Wave %d (%d task%s)", i+1, len(wave), plural(len(wave)))))
		b.WriteString("\n")
		for _, id := range wave {
			b.WriteString(richTaskRow(wf, id, idWidth))
		}
	}
	return b.String()
}

// richLoops draws one labeled group per scoped loop: its id, convergence target
// (until_empty / until), iteration cap, and every body task with its effective
// runtime/model/effort and deps, so the in-loop execution shape is visible
// without running.
func richLoops(wf *workflow.Workflow) string {
	var b strings.Builder
	b.WriteString(sectionStyle.Render(fmt.Sprintf("Loops (%d)", len(wf.Loops))))
	b.WriteString("\n")
	for _, lg := range wf.Loops {
		// idWidth is derived per loop from its own members, so a wide id in one
		// loop never pads the body columns of another.
		idWidth := 0
		for _, id := range lg.Members {
			if n := len(id); n > idWidth {
				idWidth = n
			}
		}
		b.WriteString(waveStyle.Render(fmt.Sprintf("  Loop %s (%s  max=%d, %d task%s)",
			lg.ID, loopConvergence(lg), lg.Max, len(lg.Members), plural(len(lg.Members)))))
		b.WriteString("\n")
		for _, id := range lg.Members {
			b.WriteString(richTaskRow(wf, id, idWidth))
		}
	}
	return b.String()
}

// richTaskRow renders a single indented task row shared by the wave and loop
// listings: a shell task shows its command, an LLM task its effective
// runtime/model/effort. Both surface incoming deps.
func richTaskRow(wf *workflow.Workflow, id workflow.TaskID, idWidth int) string {
	t := wf.ByID(id)
	if t == nil {
		return ""
	}
	if t.IsShell() {
		cmd := t.Command
		if len(cmd) > 60 {
			cmd = cmd[:60] + "…"
		}
		return fmt.Sprintf("    %-*s  kind=shell  cmd=%q  deps=%s\n",
			idWidth, id, cmd, depsList(t.DependsOn))
	}
	rt, m, e := wf.Effective(t)
	return fmt.Sprintf("    %-*s  runtime=%-12s  model=%-8s  effort=%-7s  deps=%s\n",
		idWidth, id, orDash(string(rt)), orDash(string(m)), orDash(string(e)), depsList(t.DependsOn))
}
