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
// deps so the real parallelism is visible at a glance. Scoped loops render as
// their own group inline at the wave where their body first becomes runnable,
// so the loop's position in the flow is visible rather than detached.
func richWaves(wf *workflow.Workflow) string {
	waves := wf.Waves()
	waveOf := waveIndex(wf)

	// idWidth is just the widest top-level task id; loop members are drawn under
	// their own groups and never appear in the wave rows, so excluding them keeps
	// this column from being padded for ids it never shows.
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

	// Filter loop members out of each wave; an original wave with no top-level
	// task (every member pulled into a loop) is dropped and the displayed waves
	// renumbered, but its position is still used to anchor inline loop groups.
	topByWave := make([][]workflow.TaskID, len(waves))
	displayed := 0
	for wi, wave := range waves {
		for _, id := range wave {
			if t := wf.ByID(id); t != nil && t.Loop == "" {
				topByWave[wi] = append(topByWave[wi], id)
			}
		}
		if len(topByWave[wi]) > 0 {
			displayed++
		}
	}

	var b strings.Builder
	b.WriteString(sectionStyle.Render(fmt.Sprintf("Execution plan (%d wave%s)", displayed, plural(displayed))))
	b.WriteString("\n")
	waveNo := 0
	for wi := range waves {
		if len(topByWave[wi]) > 0 {
			waveNo++
			b.WriteString(waveStyle.Render(fmt.Sprintf("  Wave %d (%d task%s)", waveNo, len(topByWave[wi]), plural(len(topByWave[wi])))))
			b.WriteString("\n")
			for _, id := range topByWave[wi] {
				b.WriteString(richTaskRow(wf, id, idWidth))
			}
		}
		for li := range wf.Loops {
			if loopWaveIndex(&wf.Loops[li], waveOf) == wi {
				b.WriteString(richLoopGroup(wf, &wf.Loops[li]))
			}
		}
	}
	return b.String()
}

// waveIndex maps each task id to the index of the wave it executes in, derived
// from wf.Waves(). Loop members are included so a loop's flow position can be
// computed from its body.
func waveIndex(wf *workflow.Workflow) map[workflow.TaskID]int {
	m := make(map[workflow.TaskID]int)
	for i, wave := range wf.Waves() {
		for _, id := range wave {
			m[id] = i
		}
	}
	return m
}

// loopWaveIndex returns the index of the earliest wave in which any member of
// lg becomes runnable, i.e. where the loop sits in the execution flow. A loop
// with no resolvable member wave (none in waveOf) anchors at wave 0.
func loopWaveIndex(lg *workflow.LoopGroup, waveOf map[workflow.TaskID]int) int {
	idx := -1
	for _, m := range lg.Members {
		if w, ok := waveOf[m]; ok && (idx == -1 || w < idx) {
			idx = w
		}
	}
	if idx == -1 {
		return 0
	}
	return idx
}

// richLoopGroup draws one labeled group for a scoped loop: its id, a
// kind-specific summary (a while loop's convergence target and cap, or a
// for_each loop's variable and list source), optional description, and every
// body task with its effective runtime/model/effort and deps, so the in-loop
// execution shape is visible without running. Rendered inline among the waves
// by richWaves at the loop's flow position.
func richLoopGroup(wf *workflow.Workflow, lg *workflow.LoopGroup) string {
	var b strings.Builder
	// idWidth is derived per loop from its own members, so a wide id in one loop
	// never pads the body columns of another.
	idWidth := 0
	for _, id := range lg.Members {
		if n := len(id); n > idWidth {
			idWidth = n
		}
	}
	b.WriteString(waveStyle.Render(fmt.Sprintf("  Loop %s (%s, %d task%s)",
		lg.ID, loopDescriptor(*lg), len(lg.Members), plural(len(lg.Members)))))
	b.WriteString("\n")
	if lg.Description != "" {
		b.WriteString(fmt.Sprintf("    %s %s\n", labelStyle.Render("desc"), lg.Description))
	}
	for _, id := range lg.Members {
		b.WriteString(richTaskRow(wf, id, idWidth))
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
