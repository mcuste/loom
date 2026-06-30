package scheduler

import (
	"io"
	"testing"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/store"
)

// requiredParamShellWorkflow declares a required param with no default, so a
// fire that supplies no params fails at resolution before any task runs.
const requiredParamShellWorkflow = `
name: shellwf
params:
  - name: env
    required: true
tasks:
  - id: a
    command: echo hi
`

// TestDaemonExecuteUnloadableWorkflowRecordsNoRun pins execute's load-error
// branch: when a due schedule's workflow file no longer parses, the fire
// surfaces the load error, records no run, and leaves LastRunID empty rather
// than stamping a phantom run id onto the schedule.
func TestDaemonExecuteUnloadableWorkflowRecordsNoRun(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, "name: shellwf\ntasks: [unterminated")

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

	d := New(home, io.Discard, testLoader)
	d.now = fixedClock("2026-06-28T10:01:05Z")

	results := make(chan fireResult, 1)
	d.scan(false, results)

	res := awaitResult(t, results)
	if res.err == nil {
		t.Fatal("fire result err = nil, want a workflow load error")
	}
	if res.runID != "" {
		t.Fatalf("runID = %q, want empty (load failed before any run started)", res.runID)
	}
	d.complete(res)

	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (load failed)", len(runs))
	}
	got, err := schedule.Get(home, added.ID)
	if err != nil {
		t.Fatalf("Get after failed fire: %v", err)
	}
	if got.LastRunID != "" {
		t.Fatalf("LastRunID = %q, want empty (no run was recorded)", got.LastRunID)
	}
}

// TestDaemonExecuteMissingRequiredParamRecordsNoRun pins execute's param-
// resolve-error branch: a due schedule whose workflow declares a required param
// the record never supplies fails at resolution, records no run, and leaves
// LastRunID empty.
func TestDaemonExecuteMissingRequiredParamRecordsNoRun(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, requiredParamShellWorkflow)

	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    true,
		// Params omitted: the required `env` param is never supplied.
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := New(home, io.Discard, testLoader)
	d.now = fixedClock("2026-06-28T10:01:05Z")

	results := make(chan fireResult, 1)
	d.scan(false, results)

	res := awaitResult(t, results)
	if res.err == nil {
		t.Fatal("fire result err = nil, want a param-resolution error")
	}
	if res.runID != "" {
		t.Fatalf("runID = %q, want empty (param resolution failed before any run started)", res.runID)
	}
	d.complete(res)

	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (param resolution failed)", len(runs))
	}
	got, err := schedule.Get(home, added.ID)
	if err != nil {
		t.Fatalf("Get after failed fire: %v", err)
	}
	if got.LastRunID != "" {
		t.Fatalf("LastRunID = %q, want empty (no run was recorded)", got.LastRunID)
	}
}
