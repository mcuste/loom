package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/store"
)

// fixedClock returns a deterministic clock so the daemon's firing decision is
// reproducible without depending on wall time.
func fixedClock(ts string) func() time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

// counterRand returns a deterministic six-hex suffix for schedule ids.
func counterRand(initial uint32) func() (string, error) {
	var n atomic.Uint32
	n.Store(initial)
	return func() (string, error) {
		v := n.Add(1) - 1
		return fmt.Sprintf("%06x", v), nil
	}
}

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

	d := newDaemon(home, io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z") // past the 10:01:00 tick

	results := make(chan fireResult, 1)
	d.scan(false, results)

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

// TestDaemonScanSkipsWhenRunning pins the overlap=skip policy: a schedule whose
// previous run is still in flight does not fire again and advances past the tick.
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

	d := newDaemon(home, io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")
	d.running.mark(added.ID) // pretend a prior run is still going

	results := make(chan fireResult, 1)
	d.scan(false, results)

	select {
	case <-results:
		t.Fatal("a fire was launched despite overlap=skip with a run in flight")
	case <-time.After(150 * time.Millisecond):
	}
	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (skipped)", len(runs))
	}
	// NextFire still advanced past the skipped tick.
	got, _ := schedule.Get(home, added.ID)
	if !got.NextFire.After(d.now()) {
		t.Fatalf("NextFire %v not advanced past now %v", got.NextFire, d.now())
	}
}

func awaitResult(t *testing.T, results <-chan fireResult) fireResult {
	t.Helper()
	select {
	case res := <-results:
		return res
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for fire result")
		return fireResult{}
	}
}
