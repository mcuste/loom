package scheduler

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/store"
)

// shellWorkflow is a minimal workflow that runs a shell command, so no model
// call is needed to exercise the daemon's fire path.
const shellWorkflow = `
name: shellwf
tasks:
  - id: a
    command: echo hi
`

// TestDaemonScanFiresDueSchedule wires scan -> execute -> complete end to end
// with a shell workflow so no model call is needed, and asserts a run record is
// written with the scheduled provenance.
func TestDaemonScanFiresDueSchedule(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, shellWorkflow)

	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    true,
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := newTestDaemon(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z") // past the 10:01:00 tick

	results := make(chan fireResult, 1)
	d.scan(context.Background(), false, results)

	res := awaitResult(t, results)
	if res.err != nil {
		t.Fatalf("fire error: %v", res.err)
	}
	d.complete(res)

	runs, err := store.ListRuns(home, "shellwf")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	rec, err := store.Load(runs[0].Path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rec.TriggeredBy != "schedule" || rec.ScheduleID != added.ID {
		t.Fatalf("provenance = %q/%q, want schedule/%s", rec.TriggeredBy, rec.ScheduleID, added.ID)
	}
	if rec.Status != store.StatusOK {
		t.Fatalf("run status = %q, want ok", rec.Status)
	}
	// The cron schedule survives with an advanced NextFire and a recorded run.
	got, err := schedule.Get(home, added.ID)
	if err != nil {
		t.Fatalf("Get after fire: %v", err)
	}
	if got.LastRunID != rec.RunID {
		t.Fatalf("LastRunID = %q, want %q", got.LastRunID, rec.RunID)
	}
	if !got.NextFire.After(d.now()) {
		t.Fatalf("NextFire %v not advanced past now %v", got.NextFire, d.now())
	}
	// A per-fire log file was written.
	logs, _ := filepath.Glob(filepath.Join(home, "schedules", "logs", added.ID, "*.log"))
	if len(logs) != 1 {
		t.Fatalf("got %d log files, want 1", len(logs))
	}
}

// TestDaemonScanSkipsWhenRunning pins the overlap=skip policy: a schedule
// whose previous run is still in flight does not fire again and advances past
// the tick.
func TestDaemonScanSkipsWhenRunning(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, shellWorkflow)
	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Overlap:    schedule.OverlapSkip,
		Enabled:    true,
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := newTestDaemon(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")
	d.running.mark(added.ID) // pretend a prior run is still going

	results := make(chan fireResult, 1)
	d.scan(context.Background(), false, results)

	// The skip policy launches no `go execute`, so no run goroutine is ever
	// started and the empty run index is deterministic the moment scan returns.
	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (skipped)", len(runs))
	}
	// NextFire still advanced past the skipped tick.
	got, _ := schedule.Get(home, added.ID)
	if !got.NextFire.After(d.now()) {
		t.Fatalf("NextFire %v not advanced past now %v", got.NextFire, d.now())
	}
}

// TestDaemonScanQueueHoldsThenFires pins the overlap=queue policy: a fire
// whose previous run is still in flight is HELD (not launched, NextFire left
// untouched so it stays due) and then fires on the next scan once the
// in-flight run clears.
func TestDaemonScanQueueHoldsThenFires(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, shellWorkflow)
	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Overlap:    schedule.OverlapQueue,
		Enabled:    true,
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	dueAt, _ := schedule.Get(home, added.ID)

	d := newTestDaemon(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")
	d.running.mark(added.ID) // a prior run is still going

	results := make(chan fireResult, 1)
	d.scan(context.Background(), false, results)

	// Held: the queue policy launches no `go execute` while the prior run is
	// in flight, so the empty run index is deterministic the moment scan returns.
	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (held)", len(runs))
	}
	// Unlike skip, queue leaves NextFire untouched so the tick stays due.
	if got, _ := schedule.Get(home, added.ID); !got.NextFire.Equal(dueAt.NextFire) {
		t.Fatalf("NextFire = %v, want it held at %v (queue must not advance past the tick)", got.NextFire, dueAt.NextFire)
	}

	// The prior run completes; the next scan fires the held tick.
	d.running.clear(added.ID)
	d.scan(context.Background(), false, results)
	res := awaitResult(t, results)
	if res.err != nil {
		t.Fatalf("fire error: %v", res.err)
	}
	d.complete(res)
	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 1 {
		t.Fatalf("got %d runs, want 1 after the held tick fired", len(runs))
	}
}

// TestDaemonScanAllowFiresWhileRunning pins the overlap=allow policy: a fire
// is launched even though a previous run for the same schedule is still in
// flight.
func TestDaemonScanAllowFiresWhileRunning(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, shellWorkflow)
	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Overlap:    schedule.OverlapAllow,
		Enabled:    true,
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := newTestDaemon(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")
	d.running.mark(added.ID) // a prior run is still going; allow ignores it

	results := make(chan fireResult, 1)
	d.scan(context.Background(), false, results)

	res := awaitResult(t, results)
	if res.err != nil {
		t.Fatalf("fire error: %v", res.err)
	}
	d.complete(res)
	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 1 {
		t.Fatalf("got %d runs, want 1 (allow fires regardless of the in-flight run)", len(runs))
	}
}

// TestDaemonScanSkipsDisabledSchedule pins that scan ignores a disabled
// record: a due tick on a disabled schedule launches no fire and writes no
// run, even though its cron time has passed.
func TestDaemonScanSkipsDisabledSchedule(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, shellWorkflow)
	_, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    false, // disabled: scan must skip it
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := newTestDaemon(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z") // past the 10:01:00 tick

	results := make(chan fireResult, 1)
	d.scan(context.Background(), false, results)

	// A disabled schedule is skipped before any `go execute`, so the empty run
	// index is deterministic the moment scan returns.
	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (disabled)", len(runs))
	}
}

// TestDaemonRunLoopFiresThenStopsOnCancel drives the real scan/sleep loop (not
// scan/complete in isolation): it scans a due schedule, fires it in a
// goroutine, folds the run id back through complete, and returns nil when ctx
// is cancelled.
func TestDaemonRunLoopFiresThenStopsOnCancel(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, shellWorkflow)
	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Catchup:    true, // the loop's first scan applies catch-up; fire the due tick
		Enabled:    true,
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := newTestDaemon(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// The loop fires the due schedule and folds the run id back via complete.
	// Wait on that observable record state rather than a fixed sleep.
	waitForCondition(t, func() bool {
		rec, err := schedule.Get(home, added.ID)
		return err == nil && rec.LastRunID != ""
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil after ctx cancel", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 1 {
		t.Fatalf("got %d runs, want 1 from the loop's fire", len(runs))
	}
}
