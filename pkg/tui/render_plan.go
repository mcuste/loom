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

// rich plan styles. Colors degrade to plain text under the Ascii profile, so
// the snapshot stays deterministic while terminals still get color.
//
// These styles render against lipgloss's global color profile, which is process
// state set via lipgloss.SetColorProfile. Callers that need deterministic output
// (tests, snapshots, piped output) MUST pin the profile first; otherwise the
// rendering inherits whatever profile was last set in the process.
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
	return b.String()
}

// richHeaderCard boxes the workflow identity: id, description, the effective
// runtime/model/effort triple, and the optional loop marker.
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
	if wf.Loop != nil {
		rows = append(rows, fmt.Sprintf("%s until_empty=%s max=%d",
			labelStyle.Render("loop"), wf.Loop.UntilEmpty, wf.Loop.Max))
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

	// idWidth is just the widest task id; derive it from the declared tasks
	// rather than re-running Plan (Waves already invoked it).
	idWidth := 0
	for _, t := range wf.Tasks {
		if n := len(t.ID); n > idWidth {
			idWidth = n
		}
	}

	var b strings.Builder
	b.WriteString(sectionStyle.Render(fmt.Sprintf("Execution plan (%d wave%s)", len(waves), plural(len(waves)))))
	b.WriteString("\n")
	for i, wave := range waves {
		b.WriteString(waveStyle.Render(fmt.Sprintf("  Wave %d (%d task%s)", i+1, len(wave), plural(len(wave)))))
		b.WriteString("\n")
		for _, id := range wave {
			t := wf.ByID(id)
			if t.IsShell() {
				cmd := t.Command
				if len(cmd) > 60 {
					cmd = cmd[:60] + "…"
				}
				fmt.Fprintf(&b, "    %-*s  kind=shell  cmd=%q  deps=%s\n",
					idWidth, id, cmd, depsList(t.DependsOn))
				continue
			}
			rt, m, e := wf.Effective(t)
			fmt.Fprintf(&b, "    %-*s  runtime=%-12s  model=%-8s  effort=%-7s  deps=%s\n",
				idWidth, id, orDash(string(rt)), orDash(string(m)), orDash(string(e)), depsList(t.DependsOn))
		}
	}
	return b.String()
}
