package schedule

import "time"

// Due reports whether the schedule is due to fire at now, whether to remove
// the record after firing, the next NextFire to persist, and any error.
// firstScan distinguishes a tick that is due now from one that was missed
// while the daemon was down (only honored for catch-up). This encapsulates
// the firing decision next to the Record type so the daemon needs
// only policy and side-effects, not timing logic.
func (r Record) Due(now time.Time, firstScan bool) (fire, remove bool, next time.Time, err error) {
	if r.Trigger.IsCron() {
		nf := r.NextFire
		if nf.IsZero() {
			if nf, err = r.NextFireAfter(now); err != nil {
				return false, false, time.Time{}, err
			}
		}
		if now.Before(nf) {
			return false, false, nf, nil // not due
		}
		advanced, err := r.NextFireAfter(now)
		if err != nil {
			return false, false, time.Time{}, err
		}
		if firstScan && !r.Catchup {
			// Missed tick(s) while down; skip without firing.
			return false, false, advanced, nil
		}
		return true, false, advanced, nil
	}

	// One-off.
	at := r.Trigger.At
	if now.Before(at) {
		return false, false, at, nil // not due
	}
	if firstScan && !r.Catchup {
		return false, true, time.Time{}, nil // missed while down, drop
	}
	return true, true, time.Time{}, nil // fire then remove
}
