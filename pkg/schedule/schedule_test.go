package schedule_test

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/workflow"
)

// fixedClock returns a deterministic timestamp so tests can pin CreatedAt and
// the seeded NextFire without depending on wall-clock behavior.
func fixedClock(ts string) func() time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

// counterRand returns a deterministic six-hex suffix so an assigned id is
// reproducible across runs.
func counterRand(initial uint32) func() (string, error) {
	var n atomic.Uint32
	n.Store(initial)
	return func() (string, error) {
		v := n.Add(1) - 1
		return fmt.Sprintf("%06x", v), nil
	}
}

func cronRecord() schedule.Record {
	return schedule.Record{
		WorkflowID: "deploy",
		Ref:        "deploy",
		Path:       "/abs/deploy.yaml",
		Trigger:    schedule.Trigger{Cron: "0 15 * * *", TZ: "UTC"},
		Enabled:    true,
	}
}

func TestAddAssignsIDAndNextFire(t *testing.T) {
	root := t.TempDir()
	cfg := schedule.Config{Now: fixedClock("2026-06-28T10:00:00Z"), Rand: counterRand(0xa1b2c3)}

	got, err := schedule.Add(root, cronRecord(), cfg)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if want := "deploy_cron_a1b2c3"; got.ID != want {
		t.Fatalf("id = %q, want %q", got.ID, want)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not set")
	}
	// 10:00 UTC seed -> next 15:00 the same day.
	want := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)
	if !got.NextFire.Equal(want) {
		t.Fatalf("NextFire = %v, want %v", got.NextFire, want)
	}
}

func TestAddListGetRemoveRoundTrip(t *testing.T) {
	root := t.TempDir()
	cfg := schedule.Config{Now: fixedClock("2026-06-28T10:00:00Z"), Rand: counterRand(1)}

	added, err := schedule.Add(root, cronRecord(), cfg)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := schedule.Get(root, added.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Trigger.Cron != "0 15 * * *" || got.WorkflowID != "deploy" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	list, err := schedule.List(root, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != added.ID {
		t.Fatalf("List = %+v, want one record %q", list, added.ID)
	}
	if err := schedule.Remove(root, added.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := schedule.Get(root, added.ID); err == nil {
		t.Fatal("Get after Remove: want error, got nil")
	}
}

func TestListFiltersByWorkflow(t *testing.T) {
	root := t.TempDir()
	cfg := schedule.Config{Now: fixedClock("2026-06-28T10:00:00Z"), Rand: counterRand(1)}
	if _, err := schedule.Add(root, cronRecord(), cfg); err != nil {
		t.Fatalf("Add deploy: %v", err)
	}
	other := cronRecord()
	other.WorkflowID = "report"
	if _, err := schedule.Add(root, other, cfg); err != nil {
		t.Fatalf("Add report: %v", err)
	}
	list, err := schedule.List(root, "report")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].WorkflowID != "report" {
		t.Fatalf("filtered List = %+v, want one report record", list)
	}
}

func TestUpdateRequiresExisting(t *testing.T) {
	root := t.TempDir()
	rec := cronRecord()
	rec.ID = "deploy_cron_missing"
	if err := schedule.Update(root, rec); err == nil {
		t.Fatal("Update missing: want error, got nil")
	}
	cfg := schedule.Config{Now: fixedClock("2026-06-28T10:00:00Z"), Rand: counterRand(1)}
	added, err := schedule.Add(root, cronRecord(), cfg)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	added.Enabled = false
	if err := schedule.Update(root, added); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := schedule.Get(root, added.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Enabled {
		t.Fatal("Enabled not persisted as false")
	}
}

func TestValidateRejectsBadTriggers(t *testing.T) {
	cases := map[string]schedule.Record{
		"both cron and at": {
			Trigger: schedule.Trigger{Cron: "0 15 * * *", At: time.Now()},
		},
		"neither": {
			Trigger: schedule.Trigger{},
		},
		"bad cron": {
			Trigger: schedule.Trigger{Cron: "not a cron"},
		},
		"bad tz": {
			Trigger: schedule.Trigger{Cron: "0 15 * * *", TZ: "Mars/Phobos"},
		},
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			if err := schedule.Validate(rec); err == nil {
				t.Fatalf("Validate(%s): want error, got nil", name)
			}
		})
	}
}

func TestNextFireAfterOneOff(t *testing.T) {
	at := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)
	rec := schedule.Record{Trigger: schedule.Trigger{At: at}}

	next, err := rec.NextFireAfter(at.Add(-time.Hour))
	if err != nil {
		t.Fatalf("NextFireAfter before: %v", err)
	}
	if !next.Equal(at) {
		t.Fatalf("NextFire = %v, want %v", next, at)
	}
	past, err := rec.NextFireAfter(at.Add(time.Hour))
	if err != nil {
		t.Fatalf("NextFireAfter after: %v", err)
	}
	if !past.IsZero() {
		t.Fatalf("past one-off NextFire = %v, want zero", past)
	}
}

func TestEffectiveOverlapDefaultsToSkip(t *testing.T) {
	if got := (schedule.Record{}).EffectiveOverlap(); got != schedule.OverlapSkip {
		t.Fatalf("EffectiveOverlap = %q, want %q", got, schedule.OverlapSkip)
	}
}

func TestParseAtTimeRollsOverToNextDay(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 28, 16, 0, 0, 0, loc) // 16:00, after 15:00

	at, err := schedule.ParseAtTime("15:00", "", loc, now)
	if err != nil {
		t.Fatalf("ParseAtTime: %v", err)
	}
	want := time.Date(2026, 6, 29, 15, 0, 0, 0, loc)
	if !at.Equal(want) {
		t.Fatalf("at = %v, want %v (next day)", at, want)
	}
}

func TestParseAtTimeHonorsExplicitDate(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 28, 16, 0, 0, 0, loc)

	at, err := schedule.ParseAtTime("09:30", "2026-07-01", loc, now)
	if err != nil {
		t.Fatalf("ParseAtTime: %v", err)
	}
	want := time.Date(2026, 7, 1, 9, 30, 0, 0, loc)
	if !at.Equal(want) {
		t.Fatalf("at = %v, want %v", at, want)
	}
}

func TestParseAtTimeRejectsInvalidInput(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 28, 16, 0, 0, 0, loc)

	cases := []struct {
		name    string
		timeStr string
		dateStr string
	}{
		{"non-numeric time", "noon", ""},
		{"out-of-range time", "25:00", ""},
		{"date-shaped time", "2026-07-01", ""},
		{"malformed date", "09:30", "07/01/2026"},
		{"out-of-range date", "09:30", "2026-13-40"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schedule.ParseAtTime(tc.timeStr, tc.dateStr, loc, now)
			if err == nil {
				t.Fatalf("ParseAtTime(%q, %q) = nil error, want rejection", tc.timeStr, tc.dateStr)
			}
		})
	}
}

func TestNewRecordDefaults(t *testing.T) {
	rec := schedule.NewRecord("mywf", "mywf", "/abs/mywf.yaml", map[string]string{"k": "v"}, true)
	if rec.WorkflowID != "mywf" {
		t.Errorf("WorkflowID = %q, want %q", rec.WorkflowID, "mywf")
	}
	if rec.Ref != "mywf" {
		t.Errorf("Ref = %q, want %q", rec.Ref, "mywf")
	}
	if rec.Path != "/abs/mywf.yaml" {
		t.Errorf("Path = %q, want %q", rec.Path, "/abs/mywf.yaml")
	}
	if !rec.Enabled {
		t.Error("Enabled = false, want true")
	}
	if !rec.Catchup {
		t.Error("Catchup = false, want true")
	}
	if rec.Params["k"] != "v" {
		t.Errorf("Params[k] = %q, want %q", rec.Params["k"], "v")
	}
}

// inlineWF builds a minimal Workflow with an inline schedule for sync tests.
func inlineWF(id, cron string) *workflow.Workflow {
	return &workflow.Workflow{
		ID:       workflow.WorkflowID(id),
		Schedule: &workflow.Schedule{Cron: cron, TZ: "UTC"},
	}
}

// bareWF builds a minimal Workflow with no inline schedule.
func bareWF(id string) *workflow.Workflow {
	return &workflow.Workflow{ID: workflow.WorkflowID(id)}
}

func TestSyncInlineAdds(t *testing.T) {
	home := t.TempDir()
	wf := inlineWF("mywf", "0 2 * * *")

	res, err := schedule.SyncInline(home, wf, "/abs/mywf.yaml", "mywf")
	if err != nil {
		t.Fatalf("SyncInline: %v", err)
	}
	if res.Action != schedule.SyncAdded {
		t.Fatalf("Action = %v, want SyncAdded", res.Action)
	}
	if res.ID != "mywf_inline" {
		t.Fatalf("ID = %q, want %q", res.ID, "mywf_inline")
	}
	if res.NextFire.IsZero() {
		t.Error("NextFire is zero after add")
	}
	rec, err := schedule.Get(home, "mywf_inline")
	if err != nil {
		t.Fatalf("Get after add: %v", err)
	}
	if !rec.Enabled {
		t.Error("added record not enabled")
	}
}

func TestSyncInlineUpdatePreservesFields(t *testing.T) {
	home := t.TempDir()
	wf := inlineWF("mywf", "0 2 * * *")

	// First sync creates the record.
	if _, err := schedule.SyncInline(home, wf, "/abs/mywf.yaml", "mywf"); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	before, err := schedule.Get(home, "mywf_inline")
	if err != nil {
		t.Fatalf("Get after add: %v", err)
	}
	// Simulate a fire event and a manual disable.
	before.LastFire = time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	before.LastRunID = "run_abc"
	before.Enabled = false
	if err := schedule.Update(home, before); err != nil {
		t.Fatalf("Update for setup: %v", err)
	}

	// Re-sync must preserve those fields.
	res, err := schedule.SyncInline(home, wf, "/abs/mywf.yaml", "mywf")
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res.Action != schedule.SyncUpdated {
		t.Fatalf("Action = %v, want SyncUpdated", res.Action)
	}
	after, err := schedule.Get(home, "mywf_inline")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if !after.LastFire.Equal(before.LastFire) {
		t.Errorf("LastFire = %v, want %v (not preserved)", after.LastFire, before.LastFire)
	}
	if after.LastRunID != "run_abc" {
		t.Errorf("LastRunID = %q, want %q (not preserved)", after.LastRunID, "run_abc")
	}
	if after.Enabled {
		t.Error("Enabled = true, want false (not preserved)")
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("CreatedAt changed: got %v, want %v", after.CreatedAt, before.CreatedAt)
	}
}

func TestSyncInlineDropRemoves(t *testing.T) {
	home := t.TempDir()

	// Add a record first.
	if _, err := schedule.SyncInline(home, inlineWF("mywf", "0 2 * * *"), "/abs/mywf.yaml", "mywf"); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Sync a workflow without a schedule block; should remove the record.
	res, err := schedule.SyncInline(home, bareWF("mywf"), "/abs/mywf.yaml", "mywf")
	if err != nil {
		t.Fatalf("drop sync: %v", err)
	}
	if res.Action != schedule.SyncRemoved {
		t.Fatalf("Action = %v, want SyncRemoved", res.Action)
	}
	if res.ID != "mywf_inline" {
		t.Errorf("ID = %q, want %q", res.ID, "mywf_inline")
	}
	if _, err := schedule.Get(home, "mywf_inline"); err == nil {
		t.Error("record still present after remove")
	}
}

func TestSyncInlineNoBlockNoop(t *testing.T) {
	home := t.TempDir()

	// Workflow with no schedule block and no prior stored record: no-op.
	res, err := schedule.SyncInline(home, bareWF("mywf"), "/abs/mywf.yaml", "mywf")
	if err != nil {
		t.Fatalf("SyncInline noop: %v", err)
	}
	if res.Action != schedule.SyncNoOp {
		t.Fatalf("Action = %v, want SyncNoOp", res.Action)
	}
	if res.ID != "" {
		t.Errorf("ID = %q, want empty for noop", res.ID)
	}
}

func TestTriggerSummary(t *testing.T) {
	cases := []struct {
		name string
		tr   schedule.Trigger
		want string
	}{
		{
			"cron no tz",
			schedule.Trigger{Cron: "0 15 * * *"},
			`cron "0 15 * * *"`,
		},
		{
			"cron with tz",
			schedule.Trigger{Cron: "0 15 * * *", TZ: "Europe/Berlin"},
			`cron "0 15 * * *" Europe/Berlin`,
		},
		{
			"at instant",
			schedule.Trigger{At: time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)},
			"at 2026-06-28 15:00 UTC",
		},
		{
			"at zero",
			schedule.Trigger{},
			"at -",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tr.Summary(); got != tc.want {
				t.Fatalf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}
