package schedule_test

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
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
