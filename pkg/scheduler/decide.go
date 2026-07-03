package scheduler

import (
	"time"

	"github.com/mcuste/loom/pkg/schedule"
)

// decideFire is the pure firing decision for one record at instant now. It
// delegates to rec.Due, which lives next to the Record type in pkg/schedule.
// firstScan distinguishes a tick that is due now from one that was missed
// while the daemon was down (only honored for catch-up).
func decideFire(rec schedule.Record, now time.Time, firstScan bool) (fire, remove bool, next time.Time, err error) {
	return rec.Due(now, firstScan)
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
