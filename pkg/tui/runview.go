package tui

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/muesli/termenv"

	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// runViewSymbols returns the badge glyph set for w's color profile, so the
// plain runs table and show dump degrade to ASCII glyphs on a pipe or a dumb
// terminal exactly as the live renderer does.
func runViewSymbols(w io.Writer) symbolSet {
	return symbolsFor(termenv.NewOutput(w).Profile)
}

// statusGlyph maps a stored run/task status to a one-rune badge from sym. A
// running or just-started record gets a neutral marker; an unknown status
// falls through to the same marker so the column never renders blank.
func statusGlyph(status string, sym symbolSet) string {
	switch status {
	case store.StatusOK:
		return sym.done
	case store.StatusFailed:
		return sym.failed
	case store.StatusRunning, store.StatusStarted:
		return sym.retry
	default:
		return sym.skipped
	}
}

// relTime renders t as a compact age relative to now ("2m ago", "3h ago",
// "4d ago"), falling back to an absolute date once a record is more than a
// week old. A zero time renders as a dash.
func relTime(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return t.Format("2006-01-02")
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// humanDur renders an elapsed millisecond count as a compact duration: whole
// seconds for runs at least a second long, millisecond precision below that,
// and a dash for a missing (zero) duration.
func humanDur(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// shortID returns the random suffix of a run id (the part after the timestamp
// prefix), prefixed with "·" for display. A run id without the expected
// "<ts>-<hex>" shape is returned verbatim so a hand-edited id still shows.
func shortID(runID string) string {
	if i := strings.LastIndexByte(runID, '-'); i >= 0 && i+1 < len(runID) {
		return "·" + runID[i+1:]
	}
	return runID
}

// taskRouting renders a task record's runtime facts for a summary line:
// sub-workflow tasks read "(subworkflow <ref>)", shell tasks "(shell)", LLM
// tasks "model/effort" (effort omitted when unset). The parsed manifest (nil
// when absent or no longer parsing) supplies the sub-workflow ref, which the
// record alone does not carry.
func taskRouting(wf *workflow.Workflow, tr store.TaskRecord) string {
	if wf != nil {
		if t := wf.ByID(workflow.TaskID(tr.ID)); t != nil && t.IsSubWorkflow() {
			return "(subworkflow " + t.Workflow + ")"
		}
	}
	if tr.Command != "" {
		return "(shell)"
	}
	m := tr.Model
	if m == "" {
		m = "-"
	}
	if tr.Effort != "" {
		return m + "/" + tr.Effort
	}
	return m
}

// RunsTable writes the plain, pipe-safe index of runs to w: one row per run
// with its status glyph, age, workflow, short id, task count, duration, and
// cost. Columns are tab-aligned. An empty slice prints a single "no runs"
// line so a fresh install reports cleanly rather than emitting nothing.
func RunsTable(w io.Writer, headers []store.RunHeader) error {
	if len(headers) == 0 {
		_, err := fmt.Fprintln(w, "no runs recorded")
		return err
	}
	sym := runViewSymbols(w)
	now := time.Now()
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tWHEN\tWORKFLOW\tRUN\tTASKS\tDURATION\tCOST")
	for _, h := range headers {
		tasks := fmt.Sprintf("%d", h.TaskCount)
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t$%.4f\n",
			statusGlyph(h.Status, sym), h.Status,
			relTime(h.StartedAt, now),
			h.WorkflowID,
			shortID(h.RunID),
			tasks,
			humanDur(h.ElapsedMs),
			h.TotalCostUSD,
		)
	}
	return tw.Flush()
}

// ShowRun writes a run to w in plain, pipe-safe form: a header block, a
// per-task summary table, and, unless full is false, each task's dependencies,
// substituted prompt (or shell command), output, and error. The summary-only
// view (full == false) is the quick scan; the full view is the complete
// textual record of the run, suitable for a pager or grep.
func ShowRun(w io.Writer, rec *store.RunRecord, full bool) error {
	sym := runViewSymbols(w)
	now := time.Now()
	ew := &errWriter{w: w}
	ew.printf("Run      : %s\n", rec.RunID)
	ew.printf("Workflow : %s\n", rec.WorkflowID)
	ew.printf("Status   : %s %s\n", statusGlyph(rec.Status, sym), rec.Status)
	if rec.Error != "" {
		ew.printf("Error    : %s\n", rec.Error)
	}
	ew.printf("Started  : %s (%s)\n", rec.StartedAt.Format(time.RFC3339), relTime(rec.StartedAt, now))
	ew.printf("Duration : %s\n", humanDur(rec.ElapsedMs))
	ew.printf("Cost     : $%.6f  (%d in / %d out / %d cache-read)\n",
		rec.Usage.TotalCostUSD, rec.Usage.InputTokens, rec.Usage.OutputTokens, rec.Usage.CacheReadTokens)
	if rec.Cwd != "" {
		ew.printf("Cwd      : %s\n", rec.Cwd)
	}
	if len(rec.Params) > 0 {
		ew.printf("Params   : %s\n", paramsOneLine(rec.Params))
	}
	if ew.err != nil {
		return ew.err
	}

	// Parse the embedded manifest (best effort) so the summary and bodies can
	// annotate each task with its dependencies.
	wf := parseManifest(rec)

	if _, err := fmt.Fprintf(w, "\nTasks (%d):\n", len(rec.Tasks)); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, tr := range rec.Tasks {
		note := manifestDeps(wf, tr.ID)
		if note != "" {
			note = "  needs=" + note
		}
		if tr.Error != "" {
			note += "  " + strings.TrimSpace(tr.Error)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t$%.6f%s\n",
			statusGlyph(tr.Status, sym), tr.ID+iterSuffix(tr.Iteration),
			taskRouting(wf, tr), humanDur(tr.ElapsedMs), tr.Usage.TotalCostUSD, note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !full {
		return nil
	}

	ew = &errWriter{w: w}
	for i := range rec.Tasks {
		tr := &rec.Tasks[i]
		ew.printf("\n%s\n", strings.Repeat("─", 60))
		needs := manifestDeps(wf, tr.ID)
		if needs != "" {
			needs = "   needs: " + needs
		}
		ew.printf("%s %s  %s  %s  $%.6f%s\n",
			statusGlyph(tr.Status, sym), tr.ID+iterSuffix(tr.Iteration),
			tr.Status, humanDur(tr.ElapsedMs), tr.Usage.TotalCostUSD, needs)
		writeTaskBody(ew, tr)
	}
	return ew.err
}

// ShowTask writes one task's substituted prompt (or shell command) and its
// output to w, for piping into a pager or grep. It returns an error naming the
// available task ids when taskID is not part of rec.
func ShowTask(w io.Writer, rec *store.RunRecord, taskID string) error {
	tr := findTask(rec, taskID)
	if tr == nil {
		return fmt.Errorf("task %q not found in run %s; have: %s", taskID, rec.RunID, taskIDList(rec))
	}
	ew := &errWriter{w: w}
	writeTaskBody(ew, tr)
	return ew.err
}

// writeTaskBody prints a task's command-or-prompt, output, and error under
// labeled separators. Shared by ShowTask and ShowRun's full view so a single
// task reads identically whether shown alone or inline in a full dump.
func writeTaskBody(ew *errWriter, tr *store.TaskRecord) {
	if tr.Command != "" {
		ew.printf("── command ──────────────────────────────\n%s\n", tr.Command)
	} else if tr.Prompt != "" {
		ew.printf("── prompt ───────────────────────────────\n%s\n", tr.Prompt)
	}
	ew.printf("\n── output ───────────────────────────────\n%s\n", tr.Output)
	if tr.Error != "" {
		ew.printf("\n── error ────────────────────────────────\n%s\n", tr.Error)
	}
}

// parseManifest re-parses the run record's embedded manifest, returning nil if
// it is absent or no longer parses (the workflow schema may have moved since
// the run was recorded). Callers degrade gracefully to no dependency info.
func parseManifest(rec *store.RunRecord) *workflow.Workflow {
	if rec.Manifest == "" {
		return nil
	}
	wf, err := workflow.Parse([]byte(rec.Manifest))
	if err != nil {
		return nil
	}
	return wf
}

// manifestDeps returns the comma-joined dependencies of taskID from wf, or ""
// when wf is nil or the task has none.
func manifestDeps(wf *workflow.Workflow, taskID string) string {
	if wf == nil {
		return ""
	}
	t := wf.ByID(workflow.TaskID(taskID))
	if t == nil || len(t.DependsOn) == 0 {
		return ""
	}
	parts := make([]string, len(t.DependsOn))
	for i, d := range t.DependsOn {
		parts[i] = string(d)
	}
	return strings.Join(parts, ",")
}

// findTask returns the record for taskID (first matching iteration) or nil.
func findTask(rec *store.RunRecord, taskID string) *store.TaskRecord {
	for i := range rec.Tasks {
		if rec.Tasks[i].ID == taskID {
			return &rec.Tasks[i]
		}
	}
	return nil
}

func taskIDList(rec *store.RunRecord) string {
	seen := map[string]struct{}{}
	var ids []string
	for _, tr := range rec.Tasks {
		if _, ok := seen[tr.ID]; ok {
			continue
		}
		seen[tr.ID] = struct{}{}
		ids = append(ids, tr.ID)
	}
	return strings.Join(ids, ", ")
}

// paramsOneLine renders a params map as "k=v" pairs in key order so the line
// is stable across runs.
func paramsOneLine(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + params[k]
	}
	return strings.Join(parts, " ")
}
