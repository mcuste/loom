// Package scheduler implements the daemon loop that fires scheduled workflows.
package scheduler

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/mcuste/loom/pkg/interpreter"
	"github.com/mcuste/loom/pkg/schedule"
)

// pollCap bounds how long the daemon sleeps between scans even when the next
// fire is far off, so a schedule added while the daemon runs is picked up
// promptly without a restart.
const pollCap = 60 * time.Second

// minWait floors a computed sleep so a due-but-blocked schedule (waiting on a
// queued run) does not spin the loop.
const minWait = time.Second

// daemon owns the scheduler loop. now is injectable so tests drive the firing
// decision deterministically; running tracks in-flight fires per schedule id
// so the overlap policy can skip or serialize them.
type daemon struct {
	home     string
	cwd      string
	launcher interpreter.RunLauncher
	out      io.Writer
	now      func() time.Time
	running  *runningSet
}

// New returns a daemon ready to run. home is the loom data directory that owns
// the schedules and run records; cwd is the daemon process working directory.
// launcher is the interpreter application port used to start runs.
func New(home, cwd string, launcher interpreter.RunLauncher, out io.Writer) *daemon {
	return &daemon{
		home:     home,
		cwd:      cwd,
		launcher: launcher,
		out:      out,
		now:      time.Now,
		running:  newRunningSet(),
	}
}

// Run drives the scan/sleep loop until ctx is cancelled. The first scan
// applies catch-up handling for ticks missed while the daemon was down.
// results carries completions back so the loop can clear the running flag and
// persist LastFire/LastRunID without racing the scan's NextFire writes.
func (d *daemon) Run(ctx context.Context) error {
	d.logf("loom daemon started; schedules under %s", d.schedulesDir())
	results := make(chan fireResult, 16)
	firstScan := true
	for {
		soonest := d.scan(ctx, firstScan, results)
		firstScan = false

		wait := pollCap
		if !soonest.IsZero() {
			if w := soonest.Sub(d.now()); w < wait {
				wait = w
			}
		}
		if wait < minWait {
			wait = minWait
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			d.logf("loom daemon stopping")
			return nil
		case <-timer.C:
		case res := <-results:
			timer.Stop()
			d.complete(res)
		}
	}
}

// scan evaluates every enabled schedule once and fires those that are due,
// returning the soonest future NextFire so the caller knows how long to sleep.
// It owns all NextFire writes; LastFire/LastRunID writes happen in complete.
func (d *daemon) scan(ctx context.Context, firstScan bool, results chan<- fireResult) time.Time {
	recs, err := schedule.List(d.home, "")
	if err != nil {
		d.logf("scan: %v", err)
		return time.Time{}
	}
	now := d.now()
	var soonest time.Time
	for _, rec := range recs {
		if !rec.Enabled {
			continue
		}
		soonest = earliest(soonest, d.processRecord(ctx, rec, now, firstScan, results))
	}
	return soonest
}

// processRecord evaluates one enabled schedule and acts on the firing
// decision: it drops a missed one-off, fires (or holds) a due schedule, or
// persists a freshly computed NextFire for one not yet due. It returns this
// record's contribution to the scan's soonest-NextFire, or the zero time when
// it contributes none (a decision error or a dropped one-off).
func (d *daemon) processRecord(ctx context.Context, rec schedule.Record, now time.Time, firstScan bool, results chan<- fireResult) time.Time {
	fire, remove, next, err := decideFire(rec, now, firstScan)
	if err != nil {
		d.logf("schedule %s: %v", rec.ID, err)
		return time.Time{}
	}
	if remove && !fire {
		// A one-off whose instant was missed while the daemon was down and that
		// opted out of catch-up: drop it without running.
		d.logf("schedule %s: one-off time passed while daemon was down, dropping", rec.ID)
		d.removeRecord(rec.ID)
		return time.Time{}
	}
	if fire {
		if !d.startFire(ctx, rec, now, remove, next, results) {
			// Blocked by the overlap policy (queue): leave NextFire untouched so
			// the next scan retries once the in-flight run completes.
			return now.Add(minWait)
		}
		return next
	}
	// Not due yet: persist the (possibly freshly computed) NextFire so the
	// table and the next scan agree.
	if !next.Equal(rec.NextFire) {
		rec.NextFire = next
		d.updateRecord(rec)
	}
	return next
}

// startFire applies the overlap policy and, if clear to run, marks the
// schedule running, persists the post-fire record state, and launches the
// run. It returns false when the queue policy must hold the fire for a later
// scan.
func (d *daemon) startFire(ctx context.Context, rec schedule.Record, now time.Time, remove bool, next time.Time, results chan<- fireResult) bool {
	switch rec.EffectiveOverlap() {
	case schedule.OverlapSkip:
		if d.running.active(rec.ID) {
			d.logf("schedule %s: previous run still in flight, skipping this fire", rec.ID)
			// Advance past this tick so skip means skip, not retry.
			d.advanceCron(rec, next)
			return true
		}
	case schedule.OverlapQueue:
		if d.running.active(rec.ID) {
			return false // hold; retry on next scan after the run completes
		}
	case schedule.OverlapAllow:
		// fall through: fire regardless of any in-flight run
	}

	d.running.mark(rec.ID)
	// advanceCron persists the next tick for a cron and no-ops for a one-off;
	// remove is only ever set for a one-off that just fired.
	d.advanceCron(rec, next)
	if remove {
		d.removeRecord(rec.ID)
	}
	go d.execute(ctx, rec, now, results)
	d.logf("schedule %s: firing %s", rec.ID, rec.WorkflowID)
	return true
}

// complete clears the running flag and folds the run id back into the
// schedule record (for a surviving cron schedule). One-offs were already
// removed in startFire, so a missing record here is expected and ignored.
func (d *daemon) complete(res fireResult) {
	d.running.clear(res.scheduleID)
	if res.oneOff {
		return
	}
	rec, err := schedule.Get(d.home, res.scheduleID)
	if err != nil {
		return // schedule removed meanwhile; nothing to update
	}
	rec.LastFire = res.fireTime.UTC()
	if res.runID != "" {
		rec.LastRunID = res.runID
	}
	d.updateRecord(rec)
}

// advanceCron consumes rec's current cron tick by persisting next as its
// NextFire; it is a no-op for a one-off, whose firing is recorded by removal.
func (d *daemon) advanceCron(rec schedule.Record, next time.Time) {
	if rec.Trigger.IsCron() {
		rec.NextFire = next
		d.updateRecord(rec)
	}
}

func (d *daemon) updateRecord(rec schedule.Record) {
	if err := schedule.Update(d.home, rec); err != nil {
		d.logf("schedule %s: update: %v", rec.ID, err)
	}
}

func (d *daemon) removeRecord(id string) {
	if err := schedule.Remove(d.home, id); err != nil {
		d.logf("schedule %s: remove: %v", id, err)
	}
}

func (d *daemon) logf(format string, a ...any) {
	ts := d.now().UTC().Format("2006-01-02 15:04:05")
	_, _ = fmt.Fprintf(d.out, "%s  %s\n", ts, fmt.Sprintf(format, a...))
}

// schedulesDir returns the on-disk directory that holds schedule records.
func (d *daemon) schedulesDir() string {
	return schedule.SchedulesDir(d.home)
}
