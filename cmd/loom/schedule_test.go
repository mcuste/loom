package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
)

// runCLI executes the root command with args, returning combined output and
// any error, mirroring the e2e harness used by the run tests.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

const shellWorkflow = `
name: shellwf
tasks:
  - id: a
    command: echo hi
`

func TestScheduleCronCreatesRecord(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	path := writeWorkflow(t, shellWorkflow)

	out, err := runCLI(t, "schedule", "cron", path, "--expr", "0 15 * * *", "--tz", "UTC")
	if err != nil {
		t.Fatalf("schedule cron: %v (%s)", err, out)
	}
	recs, err := schedule.List(home, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d schedules, want 1", len(recs))
	}
	r := recs[0]
	if r.WorkflowID != "shellwf" || r.Trigger.Cron != "0 15 * * *" || !r.Enabled {
		t.Fatalf("unexpected record: %+v", r)
	}
	if !strings.HasPrefix(r.Path, "/") {
		t.Errorf("Path %q is not absolute", r.Path)
	}
}

func TestScheduleEnableDisableRemove(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	path := writeWorkflow(t, shellWorkflow)

	if _, err := runCLI(t, "schedule", "cron", path, "--expr", "0 15 * * *"); err != nil {
		t.Fatalf("schedule cron: %v", err)
	}
	id := mustOneID(t, home)

	if _, err := runCLI(t, "schedule", "disable", id); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if rec, _ := schedule.Get(home, id); rec.Enabled {
		t.Fatal("schedule still enabled after disable")
	}
	if _, err := runCLI(t, "schedule", "enable", id); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if rec, _ := schedule.Get(home, id); !rec.Enabled {
		t.Fatal("schedule still disabled after enable")
	}
	if _, err := runCLI(t, "schedule", "rm", id); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if recs, _ := schedule.List(home, ""); len(recs) != 0 {
		t.Fatalf("got %d schedules after rm, want 0", len(recs))
	}
}

func TestScheduleCronRejectsBadWorkflow(t *testing.T) {
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())

	out, err := runCLI(t, "schedule", "cron", "/no/such/workflow.yaml", "--expr", "0 15 * * *")
	if err == nil {
		t.Fatalf("want error for missing workflow, got nil (%s)", out)
	}
}

func TestScheduleAtRejectsBadOverlap(t *testing.T) {
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	path := writeWorkflow(t, shellWorkflow)

	out, err := runCLI(t, "schedule", "cron", path, "--expr", "0 15 * * *", "--overlap", "bogus")
	if err == nil {
		t.Fatalf("want error for bad overlap, got nil (%s)", out)
	}
}

func TestParseAtTimeRollsOverToNextDay(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 28, 16, 0, 0, 0, loc) // 16:00, after 15:00

	at, err := parseAtTime("15:00", "", loc, now)
	if err != nil {
		t.Fatalf("parseAtTime: %v", err)
	}
	want := time.Date(2026, 6, 29, 15, 0, 0, 0, loc)
	if !at.Equal(want) {
		t.Fatalf("at = %v, want %v (next day)", at, want)
	}
}

func TestParseAtTimeHonorsExplicitDate(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 28, 16, 0, 0, 0, loc)

	at, err := parseAtTime("09:30", "2026-07-01", loc, now)
	if err != nil {
		t.Fatalf("parseAtTime: %v", err)
	}
	want := time.Date(2026, 7, 1, 9, 30, 0, 0, loc)
	if !at.Equal(want) {
		t.Fatalf("at = %v, want %v", at, want)
	}
}

const inlineScheduleWorkflow = `
name: shellwf
schedule:
  cron: "0 2 * * *"
  tz: UTC
tasks:
  - id: a
    command: echo hi
`

func TestScheduleSyncUpsertsAndRemoves(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	path := writeWorkflow(t, inlineScheduleWorkflow)

	// First sync adds the inline schedule.
	if _, err := runCLI(t, "schedule", "sync", path); err != nil {
		t.Fatalf("sync add: %v", err)
	}
	recs, _ := schedule.List(home, "")
	if len(recs) != 1 || recs[0].ID != "shellwf_inline" {
		t.Fatalf("after sync got %+v, want one shellwf_inline", recs)
	}

	// Re-sync is an idempotent update, not a duplicate.
	if _, err := runCLI(t, "schedule", "sync", path); err != nil {
		t.Fatalf("sync update: %v", err)
	}
	if recs, _ := schedule.List(home, ""); len(recs) != 1 {
		t.Fatalf("re-sync produced %d records, want 1", len(recs))
	}

	// Dropping the block and re-syncing removes the synced record.
	noSchedule := writeWorkflow(t, shellWorkflow) // same workflow id, no schedule block
	if _, err := runCLI(t, "schedule", "sync", noSchedule); err != nil {
		t.Fatalf("sync remove: %v", err)
	}
	if recs, _ := schedule.List(home, ""); len(recs) != 0 {
		t.Fatalf("after dropping block got %d records, want 0", len(recs))
	}
}

func mustOneID(t *testing.T, home string) string {
	t.Helper()
	recs, err := schedule.List(home, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d schedules, want 1", len(recs))
	}
	return recs[0].ID
}
