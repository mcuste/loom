package scheduler

import (
	"time"

	"github.com/mcuste/loom/pkg/schedule"
)

// decideFire is the pure firing decision for one record at instant now. It
// returns whether to fire, whether to remove the record, and the NextFire to
// persist. firstScan distinguishes a tick that is due now from one that was
// missed while the daemon was down (only honored for catch-up).
func decideFire(rec schedule.Record, now time.Time, firstScan bool) (fire, remove bool, next time.Time, err error) {
	if rec.Trigger.IsCron() {
		nf := rec.NextFire
		if nf.IsZero() {
			if nf, err = rec.NextFireAfter(now); err != nil {
				return false, false, time.Time{}, err
			}
		}
		if now.Before(nf) {
			return false, false, nf, nil // not due
		}
		advanced, err := rec.NextFireAfter(now)
		if err != nil {
			return false, false, time.Time{}, err
		}
		if firstScan && !rec.Catchup {
			// Missed tick(s) while down; skip without firing.
			return false, false, advanced, nil
		}
		return true, false, advanced, nil
	}

	// One-off.
	at := rec.Trigger.At
	if now.Before(at) {
		return false, false, at, nil // not due
	}
	if firstScan && !rec.Catchup {
		return false, true, time.Time{}, nil // missed while down, drop
	}
	return true, true, time.Time{}, nil // fire then remove
}

// earliest returns the earlier of two instants, treating the zero time as
// "unset" (so a real instant always wins over zero).
func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case b.Before(a):
		return b
	default:
		return a
	}
}
