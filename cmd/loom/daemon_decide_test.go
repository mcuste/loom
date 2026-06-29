package main

import (
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
)

func cronRec(next time.Time, catchup bool) schedule.Record {
	return schedule.Record{
		ID:         "wf_cron_x",
		WorkflowID: "wf",
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    true,
		Catchup:    catchup,
		NextFire:   next,
	}
}

func TestDecideFireCron(t *testing.T) {
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)

	t.Run("not due", func(t *testing.T) {
		rec := cronRec(base.Add(time.Minute), false)
		fire, remove, _, err := decideFire(rec, base, false)
		if err != nil || fire || remove {
			t.Fatalf("fire=%v remove=%v err=%v, want all false/nil", fire, remove, err)
		}
	})
	t.Run("due steady state fires and advances", func(t *testing.T) {
		rec := cronRec(base, false)
		fire, remove, next, err := decideFire(rec, base.Add(time.Second), false)
		if err != nil || !fire || remove {
			t.Fatalf("fire=%v remove=%v err=%v, want fire=true", fire, remove, err)
		}
		if !next.After(base) {
			t.Fatalf("next %v not advanced past %v", next, base)
		}
	})
	t.Run("missed tick on first scan without catchup advances without firing", func(t *testing.T) {
		rec := cronRec(base, false)
		fire, _, next, err := decideFire(rec, base.Add(10*time.Minute), true)
		if err != nil || fire {
			t.Fatalf("fire=%v err=%v, want fire=false", fire, err)
		}
		if !next.After(base.Add(10 * time.Minute).Add(-time.Minute)) {
			t.Fatalf("next %v not advanced", next)
		}
	})
	t.Run("missed tick on first scan with catchup fires", func(t *testing.T) {
		rec := cronRec(base, true)
		fire, _, _, err := decideFire(rec, base.Add(10*time.Minute), true)
		if err != nil || !fire {
			t.Fatalf("fire=%v err=%v, want fire=true", fire, err)
		}
	})
}

func TestDecideFireOneOff(t *testing.T) {
	at := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)
	rec := schedule.Record{
		ID:      "wf_at_x",
		Trigger: schedule.Trigger{At: at},
		Enabled: true,
	}

	t.Run("future not due", func(t *testing.T) {
		fire, remove, _, _ := decideFire(rec, at.Add(-time.Hour), false)
		if fire || remove {
			t.Fatalf("fire=%v remove=%v, want both false", fire, remove)
		}
	})
	t.Run("due fires and removes", func(t *testing.T) {
		fire, remove, _, _ := decideFire(rec, at.Add(time.Second), false)
		if !fire || !remove {
			t.Fatalf("fire=%v remove=%v, want both true", fire, remove)
		}
	})
	t.Run("missed on first scan without catchup drops", func(t *testing.T) {
		fire, remove, _, _ := decideFire(rec, at.Add(time.Hour), true)
		if fire || !remove {
			t.Fatalf("fire=%v remove=%v, want fire=false remove=true", fire, remove)
		}
	})
}

// TestEarliest pins the zero-time-as-unset rule: a real instant always wins
// over the zero time regardless of argument order, and of two real instants the
// earlier one is returned.
func TestEarliest(t *testing.T) {
	early := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)

	if got := earliest(time.Time{}, time.Time{}); !got.IsZero() {
		t.Errorf("earliest(zero, zero) = %v, want zero", got)
	}
	if got := earliest(time.Time{}, late); !got.Equal(late) {
		t.Errorf("earliest(zero, late) = %v, want %v", got, late)
	}
	if got := earliest(early, time.Time{}); !got.Equal(early) {
		t.Errorf("earliest(early, zero) = %v, want %v", got, early)
	}
	if got := earliest(late, early); !got.Equal(early) {
		t.Errorf("earliest(late, early) = %v, want %v", got, early)
	}
	if got := earliest(early, late); !got.Equal(early) {
		t.Errorf("earliest(early, late) = %v, want %v", got, early)
	}
}
