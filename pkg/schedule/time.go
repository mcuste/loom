package schedule

import (
	"fmt"
	"time"

	"github.com/adhocore/gronx"
)

// ParseAtTime turns a clock time (and optional date) in loc into a concrete
// instant. Without a date it uses today in loc; if that instant has already
// passed it rolls to the next day, so "at 15:00" means the next 15:00. A
// supplied date is honored verbatim (no rollover).
//
// The optional labels argument customises the field names used in error
// messages: labels[0] replaces "time" (e.g. "--time") and labels[1] replaces
// "date" (e.g. "--date"). Callers that surface CLI flag names pass them here
// so format errors name the offending flag directly.
func ParseAtTime(timeStr, dateStr string, loc *time.Location, now time.Time, labels ...string) (time.Time, error) {
	timeLabel, dateLabel := "time", "date"
	if len(labels) > 0 && labels[0] != "" {
		timeLabel = labels[0]
	}
	if len(labels) > 1 && labels[1] != "" {
		dateLabel = labels[1]
	}
	hm, err := time.Parse("15:04", timeStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("schedule: invalid %s %q: want HH:MM", timeLabel, timeStr)
	}
	if dateStr != "" {
		d, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return time.Time{}, fmt.Errorf("schedule: invalid %s %q: want YYYY-MM-DD", dateLabel, dateStr)
		}
		return time.Date(d.Year(), d.Month(), d.Day(), hm.Hour(), hm.Minute(), 0, 0, loc), nil
	}
	nowLoc := now.In(loc)
	at := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at, nil
}

// NextFireAfter computes the next instant the schedule is due strictly after t,
// returned in UTC. For a one-off, it is the trigger instant when that is after
// t, otherwise the zero time (already past). For a cron, the expression is
// evaluated in the trigger's timezone (local when TZ is empty).
func (r Record) NextFireAfter(t time.Time) (time.Time, error) {
	if !r.Trigger.IsCron() {
		if r.Trigger.At.After(t) {
			return r.Trigger.At.UTC(), nil
		}
		return time.Time{}, nil
	}
	loc := time.Local
	if r.Trigger.TZ != "" {
		l, err := time.LoadLocation(r.Trigger.TZ)
		if err != nil {
			return time.Time{}, fmt.Errorf("schedule: invalid timezone %q: %w", r.Trigger.TZ, err)
		}
		loc = l
	}
	next, err := gronx.NextTickAfter(r.Trigger.Cron, t.In(loc), false)
	if err != nil {
		return time.Time{}, fmt.Errorf("schedule: compute next tick for %q: %w", r.Trigger.Cron, err)
	}
	return next.UTC(), nil
}
