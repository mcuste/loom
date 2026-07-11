package tui

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mcuste/loom/pkg/registry"
	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/workflow"
)

// FormatScheduledTime renders an instant for display, or "-" when unset.
func FormatScheduledTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04 MST")
}

// pick returns yes when cond holds, no otherwise: a tiny ternary so a
// bool-to-label mapping reads on one line instead of a four-line if/else.
func pick(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

// SchedulesTable writes the plain, pipe-safe index of schedules to w: one row
// per record with its id, workflow, trigger summary, next scheduled time, enabled
// state, and overlap policy. An empty slice prints a single "no schedules"
// line so a fresh install reports cleanly.
func SchedulesTable(w io.Writer, recs []schedule.Record) error {
	if len(recs) == 0 {
		_, err := fmt.Fprintln(w, "no schedules")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tWORKFLOW\tTRIGGER\tNEXT RUN\tENABLED\tOVERLAP")
	for _, r := range recs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.WorkflowID, r.Trigger.Summary(), FormatScheduledTime(r.NextFire),
			pick(r.Enabled, "yes", "no"), r.EffectiveOverlap())
	}
	return tw.Flush()
}

// descWidth caps the description column so a long first line does not push the
// resolved path far off to the right.
const descWidth = 60

// WorkflowsTable writes the workflow listing to w: name, best-effort truncated
// description, and the resolved file path, columns tab-aligned. A parse error
// or absent description leaves the description column blank; an absent registry
// root lists nothing.
func WorkflowsTable(w io.Writer, refs []registry.Ref) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range refs {
		desc := ""
		if wf, perr := workflow.ParseFile(r.Path); perr == nil {
			desc = truncate(firstLine(wf.Description), descWidth)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, desc, r.Path); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// truncate shortens s to at most max runes, appending "..." when it cuts, so a
// long description stays within its column without splitting a multibyte rune.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 3 {
		return string(r[:max])
	}
	return string(r[:max-3]) + "..."
}

// firstLine returns s up to its first newline, trimmed, so a multi-line
// description collapses to a single listing column.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
