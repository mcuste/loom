package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

// pollCap bounds how long the daemon sleeps between scans even when the next
// fire is far off, so a schedule added while the daemon runs is picked up
// promptly without a restart.
const pollCap = 60 * time.Second

// minWait floors a computed sleep so a due-but-blocked schedule (waiting on a
// queued run) does not spin the loop.
const minWait = time.Second

// daemon owns the scheduler loop. now is injectable so tests drive the firing
// decision deterministically; running tracks in-flight fires per schedule id so
// the overlap policy can skip or serialize them.
type daemon struct {
	home    string
	out     io.Writer
	now     func() time.Time
	running *runningSet
}

func newDaemon(home string, out io.Writer) *daemon {
	return &daemon{
		home:    home,
		out:     out,
		now:     time.Now,
		running: newRunningSet(),
	}
}

// run drives the scan/sleep loop until ctx is cancelled. The first scan applies
// catch-up handling for ticks missed while the daemon was down. results carries
// completions back so the loop can clear the running flag and persist
// LastFire/LastRunID without racing the scan's NextFire writes.
func (d *daemon) run(ctx context.Context) error {
	d.logf("loom daemon started; schedules under %s", filepath.Join(d.home, "schedules"))
	results := make(chan fireResult, 16)
	firstScan := true
	for {
		soonest := d.scan(firstScan, results)
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

// fireResult reports a finished fire back to the loop.
type fireResult struct {
	scheduleID string
	oneOff     bool
	fireTime   time.Time
	runID      string
	err        error
}

// scan evaluates every enabled schedule once and fires those that are due,
// returning the soonest future NextFire so the caller knows how long to sleep.
// It owns all NextFire writes; LastFire/LastRunID writes happen in complete.
func (d *daemon) scan(firstScan bool, results chan<- fireResult) time.Time {
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
		soonest = earliest(soonest, d.processRecord(rec, now, firstScan, results))
	}
	return soonest
}

// processRecord evaluates one enabled schedule and acts on the firing decision:
// it drops a missed one-off, fires (or holds) a due schedule, or persists a
// freshly computed NextFire for one not yet due. It returns this record's
// contribution to the scan's soonest-NextFire, or the zero time when it
// contributes none (a decision error or a dropped one-off).
func (d *daemon) processRecord(rec schedule.Record, now time.Time, firstScan bool, results chan<- fireResult) time.Time {
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
		if !d.startFire(rec, now, remove, next, results) {
			// Blocked by the overlap policy (queue): leave NextFire untouched so
			// the next scan retries once the in-flight run completes.
			return now.Add(minWait)
		}
		return next
	}
	// Not due yet: persist the (possibly freshly computed) NextFire so the table
	// and the next scan agree.
	if !next.Equal(rec.NextFire) {
		rec.NextFire = next
		d.updateRecord(rec)
	}
	return next
}

// startFire applies the overlap policy and, if clear to run, marks the schedule
// running, persists the post-fire record state, and launches the run. It
// returns false when the queue policy must hold the fire for a later scan.
func (d *daemon) startFire(rec schedule.Record, now time.Time, remove bool, next time.Time, results chan<- fireResult) bool {
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
	go d.execute(rec, now, results)
	d.logf("schedule %s: firing %s", rec.ID, rec.WorkflowID)
	return true
}

// execute reloads the workflow fresh, resolves its params against the current
// definition, and runs it, streaming output to a per-fire log file. The run is
// recorded in the normal run store; the captured run id flows back via results.
func (d *daemon) execute(rec schedule.Record, fireTime time.Time, results chan<- fireResult) {
	res := fireResult{scheduleID: rec.ID, oneOff: !rec.Trigger.IsCron(), fireTime: fireTime}
	defer func() { results <- res }()

	wf, manifest, _, err := loadWorkflow(rec.Path)
	if err != nil {
		res.err = fmt.Errorf("load %s: %w", rec.Path, err)
		d.logf("schedule %s: %v", rec.ID, res.err)
		return
	}
	resolved, err := workflow.ResolveParams(wf, rec.Params, nil)
	if err != nil {
		res.err = fmt.Errorf("resolve params: %w", err)
		d.logf("schedule %s: %v", rec.ID, res.err)
		return
	}

	logPath, lf, err := d.openLog(rec.ID, fireTime)
	if err != nil {
		res.err = err
		d.logf("schedule %s: %v", rec.ID, err)
		return
	}
	defer func() { _ = lf.Close() }()

	cr := &captureRenderer{Renderer: tui.New(lf)}
	defer func() { _ = cr.Close() }()
	prov := provenance{scheduleID: rec.ID, triggeredBy: "schedule"}
	runErr := runWorkflow(cr, lf, runRequest{wf: wf, manifest: manifest, resolved: resolved, home: d.home, prov: prov})
	res.runID = runIDFromPath(cr.runFile)
	res.err = runErr
	if runErr != nil {
		d.logf("schedule %s: run failed (see %s): %v", rec.ID, logPath, runErr)
	} else {
		d.logf("schedule %s: run %s complete (log %s)", rec.ID, res.runID, logPath)
	}
}

// complete clears the running flag and folds the run id back into the schedule
// record (for a surviving cron schedule). One-offs were already removed in
// startFire, so a missing record here is expected and ignored.
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

// openLog creates (and returns) the per-fire log file under
// <home>/schedules/logs/<id>/<fire-timestamp>.log.
func (d *daemon) openLog(id string, fireTime time.Time) (string, *os.File, error) {
	dir := filepath.Join(d.home, "schedules", "logs", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create log dir: %w", err)
	}
	path := filepath.Join(dir, fireTime.UTC().Format("20060102T150405Z")+".log")
	f, err := os.Create(path)
	if err != nil {
		return "", nil, fmt.Errorf("create log file: %w", err)
	}
	return path, f, nil
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
