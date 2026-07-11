package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/mcuste/loom/pkg/launcher"
	"github.com/mcuste/loom/pkg/schedule"
)

var (
	createdAt = mustTime("2026-06-28T10:00:30Z")
	firstRun  = mustTime("2026-06-28T10:01:05Z")
	secondRun = mustTime("2026-06-28T10:02:05Z")
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(now time.Time) *testClock {
	return &testClock{now: now}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

type launchReply struct {
	runID string
	err   error
}

type launchCall struct {
	invocation launcher.Invocation
	provenance launcher.Provenance
	reply      chan launchReply
}

func (c launchCall) respond(runID string, err error) {
	c.reply <- launchReply{runID: runID, err: err}
}

type controlledLauncher struct {
	calls chan launchCall
}

func newControlledLauncher() *controlledLauncher {
	return &controlledLauncher{calls: make(chan launchCall, 8)}
}

func (l *controlledLauncher) Launch(ctx context.Context, invocation launcher.Invocation, provenance launcher.Provenance) (string, error) {
	call := launchCall{
		invocation: invocation,
		provenance: provenance,
		reply:      make(chan launchReply, 1),
	}
	select {
	case l.calls <- call:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case reply := <-call.reply:
		return reply.runID, reply.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type runningDaemon struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

func startDaemon(t *testing.T, home string, clock *testClock, runLauncher launcher.Runner) *runningDaemon {
	t.Helper()
	sut := New(home, "/daemon-cwd", runLauncher, io.Discard)
	sut.now = clock.Now
	ctx, cancel := context.WithCancel(context.Background())
	running := &runningDaemon{cancel: cancel, done: make(chan error, 1)}
	go func() { running.done <- sut.Run(ctx) }()
	t.Cleanup(func() { running.stop(t) })
	return running
}

func (d *runningDaemon) stop(t *testing.T) {
	t.Helper()
	d.once.Do(func() {
		d.cancel()
		select {
		case err := <-d.done:
			if err != nil {
				t.Errorf("daemon stopped with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop after cancellation")
		}
	})
}

type scheduleObserver struct {
	t       *testing.T
	home    string
	watcher *fsnotify.Watcher
}

func newScheduleObserver(t *testing.T, home string) *scheduleObserver {
	t.Helper()
	dir := schedule.SchedulesDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create schedules directory: %v", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("create schedule observer: %v", err)
	}
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		t.Fatalf("watch schedules directory: %v", err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	return &scheduleObserver{t: t, home: home, watcher: watcher}
}

func (o *scheduleObserver) awaitRecord(id string, matches func(schedule.Record) bool) schedule.Record {
	o.t.Helper()
	if rec, ok := o.matchingRecord(id, matches); ok {
		return rec
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-o.watcher.Events:
			if !ok {
				o.t.Fatal("schedule observer closed before record matched")
			}
			if rec, ok := o.matchingRecord(id, matches); ok {
				return rec
			}
		case err, ok := <-o.watcher.Errors:
			if ok {
				o.t.Fatalf("schedule observer: %v", err)
			}
		case <-timer.C:
			o.t.Fatalf("schedule %q did not reach expected state", id)
		}
	}
}

func (o *scheduleObserver) matchingRecord(id string, matches func(schedule.Record) bool) (schedule.Record, bool) {
	rec, err := schedule.Get(o.home, id)
	return rec, err == nil && matches(rec)
}

func (o *scheduleObserver) awaitRemoved(id string) {
	o.t.Helper()
	if o.recordMissing(id) {
		return
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-o.watcher.Events:
			if !ok {
				o.t.Fatal("schedule observer closed before record was removed")
			}
			if o.recordMissing(id) {
				return
			}
		case err, ok := <-o.watcher.Errors:
			if ok {
				o.t.Fatalf("schedule observer: %v", err)
			}
		case <-timer.C:
			o.t.Fatalf("schedule %q was not removed", id)
		}
	}
}

func (o *scheduleObserver) recordMissing(id string) bool {
	_, err := os.Stat(filepath.Join(schedule.SchedulesDir(o.home), id+".json"))
	return errors.Is(err, os.ErrNotExist)
}

func TestDueCronLaunchesWithScheduleProvenance(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	added := addCron(t, home, "due-cron", createdAt, schedule.OverlapSkip, true, true)
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	running := startDaemon(t, home, clock, runner)

	call := awaitLaunch(t, runner)
	if call.invocation.Ref != filepath.Join(home, "due-cron.yaml") {
		t.Errorf("workflow ref = %q, want schedule path", call.invocation.Ref)
	}
	if call.invocation.Cwd != "/daemon-cwd" {
		t.Errorf("workflow cwd = %q, want /daemon-cwd", call.invocation.Cwd)
	}
	if call.provenance.ScheduleID != added.ID || call.provenance.TriggeredBy != "schedule" {
		t.Errorf("provenance = %q/%q, want %q/schedule", call.provenance.ScheduleID, call.provenance.TriggeredBy, added.ID)
	}
	if !call.provenance.FireTime.Equal(firstRun) {
		t.Errorf("fire time = %v, want %v", call.provenance.FireTime, firstRun)
	}
	call.respond("run-1", nil)

	got := observer.awaitRecord(added.ID, func(rec schedule.Record) bool {
		return rec.LastRunID == "run-1"
	})
	if !got.LastFire.Equal(firstRun) {
		t.Errorf("LastFire = %v, want %v", got.LastFire, firstRun)
	}
	if !got.NextFire.Equal(mustTime("2026-06-28T10:02:00Z")) {
		t.Errorf("NextFire = %v, want 2026-06-28 10:02:00 UTC", got.NextFire)
	}
	running.stop(t)
}

func TestScheduleAddedWhileRunningLaunches(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	sentinel := addCron(t, home, "startup-sentinel", createdAt, schedule.OverlapSkip, false, true)
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	startDaemon(t, home, clock, runner)

	observer.awaitRecord(sentinel.ID, func(rec schedule.Record) bool {
		return rec.NextFire.Equal(mustTime("2026-06-28T10:02:00Z"))
	})
	added := addOneOff(t, home, "added-one-off", createdAt, firstRun.Add(-time.Second), false, true)

	call := awaitLaunch(t, runner)
	if call.provenance.ScheduleID != added.ID {
		t.Errorf("schedule id = %q, want %q", call.provenance.ScheduleID, added.ID)
	}
	call.respond("run-added", nil)
}

func TestSkipOverlapConsumesDueOccurrence(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	main := addCron(t, home, "skip-main", createdAt, schedule.OverlapSkip, true, true)
	sentinel := addHourlySentinel(t, home, "skip-sentinel")
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	startDaemon(t, home, clock, runner)

	first := awaitLaunch(t, runner)
	observer.awaitRecord(sentinel.ID, func(rec schedule.Record) bool {
		return rec.NextFire.Equal(mustTime("2026-06-28T11:00:00Z"))
	})
	clock.Set(secondRun)
	resetNextFire(t, home, sentinel.ID)

	observer.awaitRecord(sentinel.ID, func(rec schedule.Record) bool {
		return rec.NextFire.Equal(mustTime("2026-06-28T11:00:00Z"))
	})
	got := requireSchedule(t, home, main.ID)
	if !got.NextFire.Equal(mustTime("2026-06-28T10:03:00Z")) {
		t.Errorf("NextFire = %v, want skipped occurrence consumed", got.NextFire)
	}
	assertNoLaunch(t, runner)
	first.respond("run-skip", nil)
}

func TestQueueOverlapWaitsForActiveLaunch(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	main := addCron(t, home, "queue-main", createdAt, schedule.OverlapQueue, true, true)
	sentinel := addHourlySentinel(t, home, "queue-sentinel")
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	startDaemon(t, home, clock, runner)

	first := awaitLaunch(t, runner)
	observer.awaitRecord(sentinel.ID, func(rec schedule.Record) bool {
		return rec.NextFire.Equal(mustTime("2026-06-28T11:00:00Z"))
	})
	clock.Set(secondRun)
	resetNextFire(t, home, sentinel.ID)

	observer.awaitRecord(sentinel.ID, func(rec schedule.Record) bool {
		return rec.NextFire.Equal(mustTime("2026-06-28T11:00:00Z"))
	})
	got := requireSchedule(t, home, main.ID)
	if !got.NextFire.Equal(mustTime("2026-06-28T10:02:00Z")) {
		t.Errorf("NextFire = %v, want queued occurrence held", got.NextFire)
	}
	assertNoLaunch(t, runner)
	first.respond("run-queue-1", nil)

	second := awaitLaunch(t, runner)
	if !second.provenance.FireTime.Equal(secondRun) {
		t.Errorf("queued fire time = %v, want %v", second.provenance.FireTime, secondRun)
	}
	second.respond("run-queue-2", nil)
}

func TestAllowOverlapStartsConcurrentLaunch(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	addCron(t, home, "allow-main", createdAt, schedule.OverlapAllow, true, true)
	sentinel := addHourlySentinel(t, home, "allow-sentinel")
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	startDaemon(t, home, clock, runner)

	first := awaitLaunch(t, runner)
	observer.awaitRecord(sentinel.ID, func(rec schedule.Record) bool {
		return rec.NextFire.Equal(mustTime("2026-06-28T11:00:00Z"))
	})
	clock.Set(secondRun)
	resetNextFire(t, home, sentinel.ID)

	second := awaitLaunch(t, runner)
	first.respond("run-allow-1", nil)
	second.respond("run-allow-2", nil)
}

func TestDisabledScheduleDoesNotLaunch(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	addCron(t, home, "disabled", createdAt, schedule.OverlapSkip, true, false)
	sentinel := addCron(t, home, "disabled-sentinel", createdAt.Add(-time.Minute), schedule.OverlapSkip, false, true)
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	startDaemon(t, home, clock, runner)

	observer.awaitRecord(sentinel.ID, func(rec schedule.Record) bool {
		return rec.NextFire.Equal(mustTime("2026-06-28T10:02:00Z"))
	})
	assertNoLaunch(t, runner)
}

func TestDueOneOffLaunchesOnceAndIsRemoved(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	added := addOneOff(t, home, "due-one-off", createdAt, firstRun.Add(-time.Second), true, true)
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	startDaemon(t, home, clock, runner)

	call := awaitLaunch(t, runner)
	observer.awaitRemoved(added.ID)
	call.respond("run-one-off", nil)
}

func TestMissedOneOffWithoutCatchupIsRemoved(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	added := addOneOff(t, home, "missed-one-off", createdAt, firstRun.Add(-time.Second), false, true)
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	startDaemon(t, home, clock, runner)

	observer.awaitRemoved(added.ID)
	assertNoLaunch(t, runner)
}

func TestLauncherFailureDoesNotBlockNextOccurrence(t *testing.T) {
	home := t.TempDir()
	clock := newTestClock(firstRun)
	added := addCron(t, home, "retry-after-failure", createdAt, schedule.OverlapQueue, true, true)
	observer := newScheduleObserver(t, home)
	runner := newControlledLauncher()
	startDaemon(t, home, clock, runner)

	first := awaitLaunch(t, runner)
	clock.Set(secondRun)
	first.respond("", errors.New("launcher unavailable"))

	second := awaitLaunch(t, runner)
	second.respond("run-after-failure", nil)
	got := observer.awaitRecord(added.ID, func(rec schedule.Record) bool {
		return rec.LastRunID == "run-after-failure"
	})
	if !got.LastFire.Equal(secondRun) {
		t.Errorf("LastFire = %v, want %v", got.LastFire, secondRun)
	}
}

func addCron(t *testing.T, home, id string, now time.Time, overlap schedule.Overlap, catchup, enabled bool) schedule.Record {
	t.Helper()
	return addSchedule(t, home, schedule.Record{
		ID:         id,
		WorkflowID: id,
		Ref:        id + ".yaml",
		Path:       filepath.Join(home, id+".yaml"),
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Overlap:    overlap,
		Catchup:    catchup,
		Enabled:    enabled,
	}, now)
}

func addHourlySentinel(t *testing.T, home, id string) schedule.Record {
	t.Helper()
	return addSchedule(t, home, schedule.Record{
		ID:         id,
		WorkflowID: id,
		Ref:        id + ".yaml",
		Path:       filepath.Join(home, id+".yaml"),
		Trigger:    schedule.Trigger{Cron: "0 * * * *", TZ: "UTC"},
		Overlap:    schedule.OverlapSkip,
		Enabled:    true,
	}, createdAt.Add(-time.Minute))
}

func addOneOff(t *testing.T, home, id string, now, at time.Time, catchup, enabled bool) schedule.Record {
	t.Helper()
	return addSchedule(t, home, schedule.Record{
		ID:         id,
		WorkflowID: id,
		Ref:        id + ".yaml",
		Path:       filepath.Join(home, id+".yaml"),
		Trigger:    schedule.Trigger{At: at},
		Catchup:    catchup,
		Enabled:    enabled,
	}, now)
}

func addSchedule(t *testing.T, home string, rec schedule.Record, now time.Time) schedule.Record {
	t.Helper()
	added, err := schedule.Add(home, rec, schedule.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("add schedule %q: %v", rec.ID, err)
	}
	return added
}

func resetNextFire(t *testing.T, home, id string) {
	t.Helper()
	rec := requireSchedule(t, home, id)
	rec.NextFire = time.Time{}
	if err := schedule.Update(home, rec); err != nil {
		t.Fatalf("reset NextFire for %q: %v", id, err)
	}
}

func requireSchedule(t *testing.T, home, id string) schedule.Record {
	t.Helper()
	rec, err := schedule.Get(home, id)
	if err != nil {
		t.Fatalf("get schedule %q: %v", id, err)
	}
	return rec
}

func awaitLaunch(t *testing.T, runner *controlledLauncher) launchCall {
	t.Helper()
	select {
	case call := <-runner.calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not launch scheduled workflow")
		return launchCall{}
	}
}

func assertNoLaunch(t *testing.T, runner *controlledLauncher) {
	t.Helper()
	select {
	case call := <-runner.calls:
		t.Fatalf("unexpected launch for schedule %q", call.provenance.ScheduleID)
	default:
	}
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(fmt.Sprintf("parse test time %q: %v", value, err))
	}
	return parsed
}
