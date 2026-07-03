package scheduler

import (
	"testing"
	"time"
)

// TestEarliest pins the zero-time-as-unset rule: a real instant always wins
// over the zero time regardless of argument order, and of two real instants
// the earlier one is returned.
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
