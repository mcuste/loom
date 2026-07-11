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
// the seeded NextRunAt without depending on wall-clock behavior.
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

func cronSchedule() schedule.Schedule {
	return schedule.Schedule{
		WorkflowID: "deploy",
		Ref:        "deploy",
		Path:       "/abs/deploy.yaml",
		Trigger:    schedule.Trigger{Cron: "0 15 * * *", TZ: "UTC"},
		Enabled:    true,
	}
}

func TestAddAssignsIDAndNextRunAt(t *testing.T) {
	root := t.TempDir()
	cfg := schedule.Config{Now: fixedClock("2026-06-28T10:00:00Z"), Rand: counterRand(0xa1b2c3)}

	got, err := schedule.Add(root, cronSchedule(), cfg)
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
	if !got.NextRunAt.Equal(want) {
		t.Fatalf("NextRunAt = %v, want %v", got.NextRunAt, want)
	}
}

func TestAddNormalizesOneOffToUTC(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	at := time.Date(2026, 6, 28, 15, 0, 0, 0, loc)
	rec := schedule.Schedule{
		WorkflowID: "deploy",
		Trigger:    schedule.Trigger{At: at, TZ: "Europe/Berlin"},
		Enabled:    true,
	}

	got, err := schedule.Add(t.TempDir(), rec, schedule.Config{
		Now:  fixedClock("2026-06-28T10:00:00Z"),
		Rand: counterRand(1),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	want := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)
	if !got.Trigger.At.Equal(want) || got.Trigger.At.Location() != time.UTC {
		t.Fatalf("Trigger.At = %v in %v, want %v in UTC", got.Trigger.At, got.Trigger.At.Location(), want)
	}
	if !got.NextRunAt.Equal(want) || got.NextRunAt.Location() != time.UTC {
		t.Fatalf("NextRunAt = %v in %v, want %v in UTC", got.NextRunAt, got.NextRunAt.Location(), want)
	}
}

func TestAddListGetRemoveRoundTrip(t *testing.T) {
	root := t.TempDir()
	cfg := schedule.Config{Now: fixedClock("2026-06-28T10:00:00Z"), Rand: counterRand(1)}

	added, err := schedule.Add(root, cronSchedule(), cfg)
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
	if _, err := schedule.Add(root, cronSchedule(), cfg); err != nil {
		t.Fatalf("Add deploy: %v", err)
	}
	other := cronSchedule()
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
	rec := cronSchedule()
	rec.ID = "deploy_cron_missing"
	if err := schedule.Update(root, rec); err == nil {
		t.Fatal("Update missing: want error, got nil")
	}
	cfg := schedule.Config{Now: fixedClock("2026-06-28T10:00:00Z"), Rand: counterRand(1)}
	added, err := schedule.Add(root, cronSchedule(), cfg)
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
	cases := map[string]schedule.Schedule{
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

func TestNextRunAfterOneOff(t *testing.T) {
	at := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)
	rec := schedule.Schedule{Trigger: schedule.Trigger{At: at}}

	next, err := rec.NextRunAfter(at.Add(-time.Hour))
	if err != nil {
		t.Fatalf("NextRunAfter before: %v", err)
	}
	if !next.Equal(at) {
		t.Fatalf("NextRunAt = %v, want %v", next, at)
	}
	past, err := rec.NextRunAfter(at.Add(time.Hour))
	if err != nil {
		t.Fatalf("NextRunAfter after: %v", err)
	}
	if !past.IsZero() {
		t.Fatalf("past one-off NextRunAt = %v, want zero", past)
	}
}

func TestNextRunAfterUsesTriggerTimezone(t *testing.T) {
	rec := schedule.Schedule{Trigger: schedule.Trigger{
		Cron: "0 15 * * *",
		TZ:   "Europe/Berlin",
	}}

	next, err := rec.NextRunAfter(time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NextRunAfter: %v", err)
	}
	want := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)
	if !next.Equal(want) || next.Location() != time.UTC {
		t.Fatalf("NextRunAfter = %v in %v, want %v in UTC", next, next.Location(), want)
	}
}

func TestEffectiveOverlapDefaultsToSkip(t *testing.T) {
	if got := (schedule.Schedule{}).EffectiveOverlap(); got != schedule.OverlapSkip {
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

func TestNewScheduleDefaults(t *testing.T) {
	rec := schedule.NewSchedule("mywf", "mywf", "/abs/mywf.yaml", map[string]string{"k": "v"})
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
	if rec.Params["k"] != "v" {
		t.Errorf("Params[k] = %q, want %q", rec.Params["k"], "v")
	}
}

func TestNewCronScheduleSetsTriggerAndOverlap(t *testing.T) {
	tr := schedule.Trigger{Cron: "0 9 * * 1", TZ: "UTC"}
	rec := schedule.NewCronSchedule("deploy", "deploy", "/abs/deploy.yaml", nil, tr, schedule.OverlapQueue)
	if rec.Trigger.Cron != "0 9 * * 1" {
		t.Errorf("Trigger.Cron = %q, want %q", rec.Trigger.Cron, "0 9 * * 1")
	}
	if rec.Trigger.TZ != "UTC" {
		t.Errorf("Trigger.TZ = %q, want %q", rec.Trigger.TZ, "UTC")
	}
	if rec.Overlap != schedule.OverlapQueue {
		t.Errorf("Overlap = %q, want %q", rec.Overlap, schedule.OverlapQueue)
	}
	if !rec.Enabled {
		t.Error("Enabled = false, want true")
	}
}

func TestNewAtScheduleSetsTrigger(t *testing.T) {
	at := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	tr := schedule.Trigger{At: at, TZ: "UTC"}
	rec := schedule.NewAtSchedule("deploy", "deploy", "/abs/deploy.yaml", nil, tr)
	if !rec.Trigger.At.Equal(at) {
		t.Errorf("Trigger.At = %v, want %v", rec.Trigger.At, at)
	}
	if rec.Trigger.TZ != "UTC" {
		t.Errorf("Trigger.TZ = %q, want %q", rec.Trigger.TZ, "UTC")
	}
	if rec.Overlap != "" {
		t.Errorf("Overlap = %q, want empty (default skip)", rec.Overlap)
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
	if res.NextRunAt.IsZero() {
		t.Error("NextRunAt is zero after add")
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
	// Simulate a completed run and a manual disable.
	before.LastRunAt = time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
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
	if !after.LastRunAt.Equal(before.LastRunAt) {
		t.Errorf("LastRunAt = %v, want %v (not preserved)", after.LastRunAt, before.LastRunAt)
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
			"at instant in trigger timezone",
			schedule.Trigger{
				At: time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC),
				TZ: "Europe/Berlin",
			},
			"at 2026-06-28 15:00 CEST",
		},
		{
			"at zero",
			schedule.Trigger{},
			"at -",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.tr.Summary()
			if err != nil {
				t.Fatalf("Summary(): %v", err)
			}
			if got != tc.want {
				t.Fatalf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

func cronRec(next time.Time) schedule.Schedule {
	return schedule.Schedule{
		ID:         "wf_cron_x",
		WorkflowID: "wf",
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    true,
		NextRunAt:  next,
	}
}

func TestDueCron(t *testing.T) {
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)

	t.Run("not due", func(t *testing.T) {
		rec := cronRec(base.Add(time.Minute))
		run, remove, _, err := rec.Due(base, false)
		if err != nil || run || remove {
			t.Fatalf("run=%v remove=%v err=%v, want all false/nil", run, remove, err)
		}
	})
	t.Run("due steady state runs and advances", func(t *testing.T) {
		rec := cronRec(base)
		run, remove, next, err := rec.Due(base.Add(time.Second), false)
		if err != nil || !run || remove {
			t.Fatalf("run=%v remove=%v err=%v, want run=true", run, remove, err)
		}
		if !next.After(base) {
			t.Fatalf("next %v not advanced past %v", next, base)
		}
	})
	t.Run("missed tick on first scan advances without running", func(t *testing.T) {
		rec := cronRec(base)
		run, _, next, err := rec.Due(base.Add(10*time.Minute), true)
		if err != nil || run {
			t.Fatalf("run=%v err=%v, want run=false", run, err)
		}
		if !next.After(base.Add(10 * time.Minute).Add(-time.Minute)) {
			t.Fatalf("next %v not advanced", next)
		}
	})
}

// TestDueCronZeroNextRunAt pins the bootstrap branch: a cron record with an
// unset NextRunAt computes its first tick from now rather than treating the
// zero time as due. With now on a minute boundary the next "* * * * *" tick
// is in the future, so the record is not due and the computed tick is returned.
func TestDueCronZeroNextRunAt(t *testing.T) {
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	rec := cronRec(time.Time{}) // NextRunAt unset

	run, remove, next, err := rec.Due(base, false)
	if err != nil || run || remove {
		t.Fatalf("run=%v remove=%v err=%v, want all false/nil", run, remove, err)
	}
	if !next.After(base) {
		t.Fatalf("next %v not computed forward from %v", next, base)
	}
}

// TestDueCronBadExpr pins that an unparseable cron expression surfaces as an
// error from Due (via NextRunAfter) rather than silently skipping.
func TestDueCronBadExpr(t *testing.T) {
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	rec := schedule.Schedule{
		ID:      "wf_cron_bad",
		Trigger: schedule.Trigger{Cron: "not a cron", TZ: "UTC"},
		Enabled: true,
	}

	if _, _, _, err := rec.Due(base, false); err == nil {
		t.Fatal("Due on a malformed cron returned nil error; want a parse error")
	}
}

func TestDueOneOff(t *testing.T) {
	at := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)
	rec := schedule.Schedule{
		ID:      "wf_at_x",
		Trigger: schedule.Trigger{At: at},
		Enabled: true,
	}

	t.Run("future not due", func(t *testing.T) {
		run, remove, _, _ := rec.Due(at.Add(-time.Hour), false)
		if run || remove {
			t.Fatalf("run=%v remove=%v, want both false", run, remove)
		}
	})
	t.Run("due runs and removes", func(t *testing.T) {
		run, remove, _, _ := rec.Due(at.Add(time.Second), false)
		if !run || !remove {
			t.Fatalf("run=%v remove=%v, want both true", run, remove)
		}
	})
	t.Run("missed on first scan drops", func(t *testing.T) {
		run, remove, _, _ := rec.Due(at.Add(time.Hour), true)
		if run || !remove {
			t.Fatalf("run=%v remove=%v, want run=false remove=true", run, remove)
		}
	})
}
