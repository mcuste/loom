// Package daemon implements the loop that fires scheduled workflows.
package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/mcuste/loom/pkg/launcher"
	"github.com/mcuste/loom/pkg/schedule"
)

// reconcileInterval bounds how long the daemon trusts filesystem events before
// rescanning the schedule store as a fallback.
const reconcileInterval = 10 * time.Minute

// minWait floors a computed sleep so a due-but-blocked schedule (waiting on a
// queued run) does not spin the loop.
const minWait = time.Second

// Daemon owns the schedule-firing loop. now is injectable so tests drive the
// firing decision deterministically; running tracks in-flight fires per
// schedule id so the overlap policy can skip or serialize them.
type Daemon struct {
	home     string
	cwd      string
	launcher launcher.RunLauncher
	out      io.Writer
	now      func() time.Time
	running  *runningSet
}

// New returns a daemon ready to run. home is the loom data directory that owns
// the schedules and run records; cwd is the daemon process working directory.
// launcher is the application port used to start runs.
func New(home, cwd string, launcher launcher.RunLauncher, out io.Writer) *Daemon {
	return &Daemon{
		home:     home,
		cwd:      cwd,
		launcher: launcher,
		out:      out,
		now:      time.Now,
		running:  newRunningSet(),
	}
}

// launchResult carries a completed scheduled launch back to the loop.
type launchResult struct {
	scheduleID  string
	oneOff      bool
	scheduledAt time.Time
	runID       string
	err         error
}

// wakeSources groups the channels that can wake the daemon between scans.
type wakeSources struct {
	events  <-chan fsnotify.Event
	errors  <-chan error
	results chan launchResult
}

// Run drives the scan/wake loop until ctx is cancelled. The first scan
// applies catch-up handling for ticks missed while the daemon was down.
// results carries completions back so the loop can clear the running flag and
// persist LastFire/LastRunID without racing the scan's NextFire writes.
func (d *Daemon) Run(ctx context.Context) error {
	watcher, err := d.newScheduleWatcher()
	if err != nil {
		return err
	}
	defer func() {
		if err := watcher.Close(); err != nil {
			d.logf("watch schedules: close: %v", err)
		}
	}()

	wake := wakeSources{
		events:  watcher.Events,
		errors:  watcher.Errors,
		results: make(chan launchResult, 16),
	}
	firstScan := true
	for {
		soonest := d.scan(ctx, firstScan, wake.results)
		if firstScan {
			d.logf("loom daemon started; schedules under %s", d.schedulesDir())
		}
		firstScan = false
		if !d.waitForRescan(ctx, d.nextWait(soonest), &wake) {
			d.logf("loom daemon stopping")
			return nil
		}
	}
}

func (d *Daemon) nextWait(soonest time.Time) time.Duration {
	wait := reconcileInterval
	if !soonest.IsZero() {
		if untilNext := soonest.Sub(d.now()); untilNext < wait {
			wait = untilNext
		}
	}
	if wait < minWait {
		return minWait
	}
	return wait
}

// waitForRescan blocks until the daemon should scan again. It reports false
// when the context is cancelled and the daemon should stop.
func (d *Daemon) waitForRescan(ctx context.Context, wait time.Duration, wake *wakeSources) bool {
	timer := time.NewTimer(wait)
	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			return true
		case res := <-wake.results:
			timer.Stop()
			d.complete(res)
			return true
		case event, ok := <-wake.events:
			if !ok {
				wake.events = nil
				continue
			}
			if !isScheduleChange(event) {
				continue
			}
			timer.Stop()
			return true
		case err, ok := <-wake.errors:
			if !ok {
				wake.errors = nil
				continue
			}
			d.logf("watch schedules: %v", err)
		}
	}
}

func (d *Daemon) newScheduleWatcher() (*fsnotify.Watcher, error) {
	dir := d.schedulesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("daemon: create schedules directory: %w", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("daemon: create schedule watcher: %w", err)
	}
	if err := watcher.Add(dir); err != nil {
		if closeErr := watcher.Close(); closeErr != nil {
			d.logf("watch schedules: close after add: %v", closeErr)
		}
		return nil, fmt.Errorf("daemon: watch schedules directory: %w", err)
	}
	return watcher, nil
}

func isScheduleChange(event fsnotify.Event) bool {
	if filepath.Ext(event.Name) != ".json" {
		return false
	}
	const changes = fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	return event.Op&changes != 0
}

// scan evaluates every enabled schedule once and fires those that are due,
// returning the soonest future NextFire so the caller knows how long to sleep.
// It owns all NextFire writes; LastFire/LastRunID writes happen in complete.
func (d *Daemon) scan(ctx context.Context, firstScan bool, results chan<- launchResult) time.Time {
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

// processRecord evaluates one enabled schedule and acts on the firing
// decision: it drops a missed one-off, fires (or holds) a due schedule, or
// persists a freshly computed NextFire for one not yet due. It returns this
// record's contribution to the scan's soonest-NextFire, or the zero time when
// it contributes none (a decision error or a dropped one-off).
func (d *Daemon) processRecord(ctx context.Context, rec schedule.Record, now time.Time, firstScan bool, results chan<- launchResult) time.Time {
	fire, remove, next, err := rec.Due(now, firstScan)
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
		if !d.startScheduledRun(ctx, rec, now, remove, next, results) {
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

// startScheduledRun applies the overlap policy and, if clear to run, marks the
// schedule running, persists the post-trigger record state, and launches the
// run. It returns false when the queue policy must hold the due occurrence for
// a later scan.
func (d *Daemon) startScheduledRun(ctx context.Context, rec schedule.Record, now time.Time, remove bool, next time.Time, results chan<- launchResult) bool {
	switch rec.EffectiveOverlap() {
	case schedule.OverlapSkip:
		if d.running.contains(rec.ID) {
			d.logf("schedule %s: previous run still in flight, skipping this fire", rec.ID)
			// Advance past this tick so skip means skip, not retry.
			d.advanceCron(rec, next)
			return true
		}
	case schedule.OverlapQueue:
		if d.running.contains(rec.ID) {
			return false // hold; retry on next scan after the run completes
		}
	case schedule.OverlapAllow:
		// fall through: fire regardless of any in-flight run
	}

	d.running.add(rec.ID)
	// advanceCron persists the next tick for a cron and no-ops for a one-off;
	// remove is only ever set for a one-off that just fired.
	d.advanceCron(rec, next)
	if remove {
		d.removeRecord(rec.ID)
	}
	go d.launchScheduledRun(ctx, rec, now, results)
	d.logf("schedule %s: firing %s", rec.ID, rec.WorkflowID)
	return true
}

// launchScheduledRun sends the schedule's opaque workflow request through the
// launcher port. The daemon does not load workflow YAML, validate params,
// inspect tasks, or configure runtimes here; those are launcher concerns.
func (d *Daemon) launchScheduledRun(ctx context.Context, rec schedule.Record, scheduledAt time.Time, results chan<- launchResult) {
	res := launchResult{scheduleID: rec.ID, oneOff: !rec.Trigger.IsCron(), scheduledAt: scheduledAt}
	defer func() { results <- res }()

	if d.launcher == nil {
		res.err = fmt.Errorf("schedule %s: run launcher is nil", rec.ID)
		d.logf("schedule %s: %v", rec.ID, res.err)
		return
	}
	prov := launcher.Provenance{ScheduleID: rec.ID, TriggeredBy: "schedule", ScheduledAt: scheduledAt}
	runID, err := d.launcher.Launch(ctx, rec.RunRequest(d.cwd), prov)
	res.runID = string(runID)
	res.err = err
	if err != nil {
		d.logf("schedule %s: run failed: %v", rec.ID, err)
		return
	}
	d.logf("schedule %s: run %s complete", rec.ID, res.runID)
}

// complete removes the running marker and folds the run id back into the
// schedule record (for a surviving cron schedule). One-offs were already
// removed in startScheduledRun, so a missing record here is expected and ignored.
func (d *Daemon) complete(res launchResult) {
	d.running.remove(res.scheduleID)
	if res.oneOff {
		return
	}
	rec, err := schedule.Get(d.home, res.scheduleID)
	if err != nil {
		return // schedule removed meanwhile; nothing to update
	}
	rec.LastFire = res.scheduledAt.UTC()
	if res.runID != "" {
		rec.LastRunID = res.runID
	}
	d.updateRecord(rec)
}

// advanceCron consumes rec's current cron tick by persisting next as its
// NextFire; it is a no-op for a one-off, whose firing is recorded by removal.
func (d *Daemon) advanceCron(rec schedule.Record, next time.Time) {
	if rec.Trigger.IsCron() {
		rec.NextFire = next
		d.updateRecord(rec)
	}
}

func (d *Daemon) updateRecord(rec schedule.Record) {
	if err := schedule.Update(d.home, rec); err != nil {
		d.logf("schedule %s: update: %v", rec.ID, err)
	}
}

func (d *Daemon) removeRecord(id string) {
	if err := schedule.Remove(d.home, id); err != nil {
		d.logf("schedule %s: remove: %v", id, err)
	}
}

func (d *Daemon) logf(format string, a ...any) {
	ts := d.now().UTC().Format("2006-01-02 15:04:05")
	_, _ = fmt.Fprintf(d.out, "%s  %s\n", ts, fmt.Sprintf(format, a...))
}

// schedulesDir returns the on-disk directory that holds schedule records.
func (d *Daemon) schedulesDir() string {
	return schedule.SchedulesDir(d.home)
}

// runningSet tracks schedule ids with a run in flight. It owns the daemon's
// shared state between the scan loop and scheduled-run goroutines.
type runningSet struct {
	mu sync.Mutex
	m  map[string]bool
}

func newRunningSet() *runningSet {
	return &runningSet{m: map[string]bool{}}
}

func (s *runningSet) contains(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

func (s *runningSet) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = true
}

func (s *runningSet) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}
