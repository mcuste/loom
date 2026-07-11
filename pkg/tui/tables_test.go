package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/registry"
	"github.com/mcuste/loom/pkg/schedule"
)

// TestSchedulesTableEmpty pins the empty-set "no schedules" message.
func TestSchedulesTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := SchedulesTable(&buf, nil); err != nil {
		t.Fatalf("SchedulesTable: %v", err)
	}
	if !strings.Contains(buf.String(), "no schedules") {
		t.Errorf("empty list should print 'no schedules'; got: %q", buf.String())
	}
}

// TestSchedulesTableColumns pins the header column names and per-row field
// rendering for a populated schedule list.
func TestSchedulesTableColumns(t *testing.T) {
	recs := []schedule.Record{
		{
			ID:         "shellwf_cron_abc123",
			WorkflowID: "shellwf",
			Trigger:    schedule.Trigger{Cron: "0 15 * * *"},
			Enabled:    true,
		},
	}
	var buf bytes.Buffer
	if err := SchedulesTable(&buf, recs); err != nil {
		t.Fatalf("SchedulesTable: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"ID", "WORKFLOW", "TRIGGER", "NEXT RUN", "ENABLED", "OVERLAP",
		"shellwf_cron_abc123", "shellwf", "0 15 * * *",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SchedulesTable output missing %q:\n%s", want, out)
		}
	}
	// Enabled=true → "yes".
	if !strings.Contains(out, "yes") {
		t.Errorf("enabled=true should render as 'yes'; got:\n%s", out)
	}
	// NextFire zero → "-".
	if !strings.Contains(out, "-") {
		t.Errorf("zero NextFire should render as '-'; got:\n%s", out)
	}
}

// TestSchedulesTableDisabledRow pins that Enabled=false renders as "no".
func TestSchedulesTableDisabledRow(t *testing.T) {
	recs := []schedule.Record{
		{ID: "x", WorkflowID: "w", Trigger: schedule.Trigger{Cron: "0 0 * * *"}, Enabled: false},
	}
	var buf bytes.Buffer
	if err := SchedulesTable(&buf, recs); err != nil {
		t.Fatalf("SchedulesTable: %v", err)
	}
	if !strings.Contains(buf.String(), "no") {
		t.Errorf("enabled=false should render as 'no'; got: %q", buf.String())
	}
}

// TestFormatScheduledTimeZero pins "-" for the zero time.
func TestFormatScheduledTimeZero(t *testing.T) {
	if got := FormatScheduledTime(time.Time{}); got != "-" {
		t.Errorf("FormatScheduledTime(zero) = %q, want -", got)
	}
}

// TestFormatScheduledTimeNonZero pins the display layout for a concrete instant.
func TestFormatScheduledTimeNonZero(t *testing.T) {
	ts := time.Date(2026, 6, 30, 15, 0, 0, 0, time.UTC)
	got := FormatScheduledTime(ts)
	if !strings.HasPrefix(got, "2026-06-30 15:00") {
		t.Errorf("FormatScheduledTime = %q, want prefix 2026-06-30 15:00", got)
	}
}

// writeWFFile creates a temp YAML file with the given body and returns its
// absolute path.
func writeWFFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

// minimalWFBody returns a minimal parseable workflow body with the given
// description, so table tests can control the description column.
func minimalWFBody(desc string) string {
	return "name: wf\ndescription: " + desc + "\nruntime: cmd-echo\nmodel: m1\ntasks:\n  - id: a\n    command: echo hi\n"
}

// TestWorkflowsTableColumns pins that name and path columns appear.
func TestWorkflowsTableColumns(t *testing.T) {
	path := writeWFFile(t, "name: mywf\nruntime: cmd-echo\nmodel: m1\ntasks:\n  - id: a\n    command: echo hi\n")
	var buf bytes.Buffer
	if err := WorkflowsTable(&buf, []registry.Ref{{Name: "mywf", Path: path}}); err != nil {
		t.Fatalf("WorkflowsTable: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"mywf", path} {
		if !strings.Contains(out, want) {
			t.Errorf("WorkflowsTable missing %q:\n%s", want, out)
		}
	}
}

// TestWorkflowsTableTruncation pins that a description longer than descWidth
// is truncated with "...".
func TestWorkflowsTableTruncation(t *testing.T) {
	longDesc := strings.Repeat("x", 65) // exceeds descWidth (60)
	path := writeWFFile(t, minimalWFBody(longDesc))
	var buf bytes.Buffer
	if err := WorkflowsTable(&buf, []registry.Ref{{Name: "wf", Path: path}}); err != nil {
		t.Fatalf("WorkflowsTable: %v", err)
	}
	out := buf.String()
	truncated := strings.Repeat("x", 57) + "..."
	if !strings.Contains(out, truncated) {
		t.Errorf("long description should be truncated to %d runes with '...'; got:\n%s", descWidth, out)
	}
	if strings.Contains(out, longDesc) {
		t.Errorf("untruncated long description should not appear; got:\n%s", out)
	}
}

// TestWorkflowsTableFirstLine pins that only the first line of a multi-line
// description is shown.
func TestWorkflowsTableFirstLine(t *testing.T) {
	// Use a YAML literal block to embed a real newline in the description.
	body := "name: wf\nruntime: cmd-echo\nmodel: m1\ndescription: |\n  first\n  second\ntasks:\n  - id: a\n    command: echo hi\n"
	path := writeWFFile(t, body)
	var buf bytes.Buffer
	if err := WorkflowsTable(&buf, []registry.Ref{{Name: "wf", Path: path}}); err != nil {
		t.Fatalf("WorkflowsTable: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "first") {
		t.Errorf("first line of description should appear; got:\n%s", out)
	}
	if strings.Contains(out, "second") {
		t.Errorf("second line of multi-line description should not appear; got:\n%s", out)
	}
}

// TestWorkflowsTableBlankDescOnParseError pins that a file that fails to parse
// still produces a row in the listing, with a blank description column.
func TestWorkflowsTableBlankDescOnParseError(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(badPath, []byte("not: valid: yaml: [unclosed"), 0o644); err != nil {
		t.Fatalf("write bad yaml: %v", err)
	}
	var buf bytes.Buffer
	if err := WorkflowsTable(&buf, []registry.Ref{{Name: "bad", Path: badPath}}); err != nil {
		t.Fatalf("WorkflowsTable: %v", err)
	}
	out := buf.String()
	// Row is present with name and path.
	if !strings.Contains(out, "bad") {
		t.Errorf("bad-yaml entry should still appear; got:\n%s", out)
	}
	if !strings.Contains(out, badPath) {
		t.Errorf("bad-yaml path should appear; got:\n%s", out)
	}
}
