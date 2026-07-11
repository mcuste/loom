// Package schedule persists workflow schedules to disk so the loom daemon can
// start runs at cron times or one-off instants without the user keeping a shell
// open.
//
// Each schedule is a single self-contained JSON file under the loom home
// directory:
//
//	<root>/schedules/<id>.json
//
// where id is "<workflow_id>_<kind>_<6 random hex>" and kind is "cron" or
// "at". A record names the workflow to run (by its original ref and its
// resolved absolute path), the trigger (a cron expression or a one-off
// instant), the parameters to pass, and the overlap policy. Files are
// rewritten atomically (write to .tmp, rename) so a crash mid-write never
// leaves a half-written schedule on disk, mirroring the run store in
// [github.com/mcuste/loom/pkg/store].
//
// The package owns storage, validation, and scheduled-time computation; the
// overlap policy and run coordination live in the daemon.
package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/adhocore/gronx"

	"github.com/mcuste/loom/pkg/launcher"
	"github.com/mcuste/loom/pkg/workflow"
)

// ScheduleID identifies a persisted schedule.
type ScheduleID string

// OverlapPolicy determines what happens when a scheduled run becomes due while
// the schedule's previous run is still in flight.
type OverlapPolicy string

const (
	// OverlapSkip drops the due run if the previous run is still running. The
	// default is safest for workflows that carry cross-run state.
	OverlapSkip OverlapPolicy = "skip"
	// OverlapQueue serializes runs per schedule: the due run waits for the
	// previous run to finish.
	OverlapQueue OverlapPolicy = "queue"
	// OverlapAllow starts runs concurrently regardless of any in-flight run.
	OverlapAllow OverlapPolicy = "allow"
)

// ParseOverlap validates an overlap policy token and returns its typed value.
// The accepted set is the single authority for what `--overlap` and any other
// caller may supply, so a new policy is added here rather than in the CLI.
func ParseOverlap(s string) (OverlapPolicy, error) {
	switch OverlapPolicy(s) {
	case OverlapSkip, OverlapQueue, OverlapAllow:
		return OverlapPolicy(s), nil
	default:
		return "", fmt.Errorf("invalid overlap %q: want skip, queue, or allow", s)
	}
}

// CronTrigger is a recurring cron trigger.
type CronTrigger struct {
	Expr string
	TZ   string
}

// OneShotTrigger is a one-off trigger at an absolute instant.
type OneShotTrigger struct {
	At time.Time
}

// Trigger is a schedule's timing rule. Exactly one of Cron or At is set: Cron
// is a recurring gronx expression, At is a one-off scheduled instant (UTC).
// TZ is the IANA location name the cron expression is evaluated in; empty means
// the daemon's local time.
type Trigger struct {
	Cron string    `json:"cron,omitempty"`
	At   time.Time `json:"at,omitzero"`
	TZ   string    `json:"tz,omitempty"`
}

// NewCronTrigger adapts the explicit CronTrigger value object to the persisted
// Trigger DTO.
func NewCronTrigger(t CronTrigger) Trigger {
	return Trigger{Cron: t.Expr, TZ: t.TZ}
}

// NewOneShotTrigger adapts the explicit OneShotTrigger value object to the
// persisted Trigger DTO.
func NewOneShotTrigger(t OneShotTrigger) Trigger {
	return Trigger{At: t.At}
}

// IsCron reports whether the trigger is a recurring cron rule.
func (t Trigger) IsCron() bool { return t.Cron != "" }

// Summary renders the trigger for display: "cron <expr>" (with optional TZ)
// for a recurring trigger, or "at <instant>" for a one-off. The format of
// the instant matches the canonical "2006-01-02 15:04 MST" display layout.
func (t Trigger) Summary() string {
	if t.IsCron() {
		if t.TZ != "" {
			return fmt.Sprintf("cron %q %s", t.Cron, t.TZ)
		}
		return fmt.Sprintf("cron %q", t.Cron)
	}
	if t.At.IsZero() {
		return "at -"
	}
	return "at " + t.At.Format("2006-01-02 15:04 MST")
}

// Schedule is a single persisted workflow schedule.
type Schedule struct {
	// ID is the stable on-disk identity ("<workflow_id>_<kind>_<suffix>").
	ID string `json:"id"`
	// WorkflowID is the workflow's id, kept for display and the `ls` filter.
	WorkflowID string `json:"workflow_id"`
	// Ref is the original CLI workflow argument (a registry name or a path).
	Ref string `json:"ref"`
	// Path is the resolved absolute YAML path the daemon loads at run time, so
	// a cwd-relative registry lookup is not repeated from the daemon's cwd.
	Path string `json:"path"`
	// Trigger is the timing rule (cron or one-off).
	Trigger Trigger `json:"trigger"`
	// Params holds the resolved -p key=val values to pass to each run.
	Params map[string]string `json:"params,omitempty"`
	// Enabled gates runs: a disabled schedule is retained but never started.
	Enabled bool `json:"enabled"`
	// Overlap is the in-flight-run policy; empty means OverlapSkip.
	Overlap OverlapPolicy `json:"overlap,omitempty"`
	// NextRunAt is the next instant this schedule is due (UTC). Maintained by the
	// daemon; Add seeds it from the trigger.
	NextRunAt time.Time `json:"next_run_at,omitzero"`
	// LastRunAt is the scheduled time of the most recent run (UTC).
	LastRunAt time.Time `json:"last_run_at,omitzero"`
	// LastRunID links the schedule to its most recent record in the run store.
	LastRunID string `json:"last_run_id,omitempty"`
	// CreatedAt is when the schedule was added (UTC).
	CreatedAt time.Time `json:"created_at"`
}

// RunRequest returns the opaque workflow request for this scheduled run. The
// daemon passes this value to a launcher.RunLauncher without inspecting the
// referenced workflow's tasks, graph, runtimes, or reports.
func (r Schedule) RunRequest(defaultCwd string) launcher.RunRequest {
	ref := r.Path
	if ref == "" {
		ref = r.Ref
	}
	params := make(map[string]string, len(r.Params))
	for k, v := range r.Params {
		params[k] = v
	}
	return launcher.RunRequest{
		Ref:    ref,
		Params: params,
		Cwd:    defaultCwd,
	}
}

// EffectiveOverlap returns the record's overlap policy, defaulting an empty
// value to OverlapSkip.
func (r Schedule) EffectiveOverlap() OverlapPolicy {
	if r.Overlap == "" {
		return OverlapSkip
	}
	return r.Overlap
}

// NextRunAfter computes the next instant the schedule is due strictly after t,
// returned in UTC. For a one-off, it is the trigger instant when that is after
// t, otherwise the zero time (already past). For a cron, the expression is
// evaluated in the trigger's timezone (local when TZ is empty).
func (r Schedule) NextRunAfter(t time.Time) (time.Time, error) {
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

// Due reports whether the schedule should run at now, whether to remove the
// record afterward, the next scheduled time to persist, and any error.
// On the first scan, it skips elapsed cron times and removes elapsed one-offs
// so downtime never causes delayed runs. This keeps the timing decision next
// to Schedule so the daemon owns only policy and side effects.
func (r Schedule) Due(now time.Time, firstScan bool) (run, remove bool, next time.Time, err error) {
	if r.Trigger.IsCron() {
		nf := r.NextRunAt
		if nf.IsZero() {
			if nf, err = r.NextRunAfter(now); err != nil {
				return false, false, time.Time{}, err
			}
		}
		if now.Before(nf) {
			return false, false, nf, nil // not due
		}
		advanced, err := r.NextRunAfter(now)
		if err != nil {
			return false, false, time.Time{}, err
		}
		if firstScan {
			// Missed tick(s) while down; advance without starting a run.
			return false, false, advanced, nil
		}
		return true, false, advanced, nil
	}

	// One-off.
	at := r.Trigger.At
	if now.Before(at) {
		return false, false, at, nil // not due
	}
	if firstScan {
		return false, true, time.Time{}, nil // missed while down, drop
	}
	return true, true, time.Time{}, nil // run then remove
}

// Config is the optional configuration for [Add]. The zero value is valid.
type Config struct {
	// Now overrides the clock used for CreatedAt and the initial NextRunAt; nil
	// uses time.Now. Tests inject a fixed clock for determinism.
	Now func() time.Time
	// Rand returns the random suffix appended to a generated id. nil uses
	// crypto/rand; tests inject a deterministic source.
	Rand func() (string, error)
}

// SyncAction describes the outcome of a [SyncInline] call.
type SyncAction int

const (
	// SyncNoOp means the workflow has no inline schedule and none was stored.
	SyncNoOp SyncAction = iota
	// SyncAdded means a new inline schedule record was created.
	SyncAdded
	// SyncUpdated means an existing inline schedule record was updated in place.
	SyncUpdated
	// SyncRemoved means a previously synced inline schedule was deleted because
	// the workflow's `schedule:` block was dropped.
	SyncRemoved
)

// SyncResult is the outcome of a [SyncInline] call.
type SyncResult struct {
	// Action classifies what happened.
	Action SyncAction
	// ID is the schedule id that was affected. Empty for SyncNoOp.
	ID string
	// NextRunAt is the next scheduled instant for SyncAdded and SyncUpdated.
	// Zero for SyncRemoved and SyncNoOp.
	NextRunAt time.Time
}

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

// Validate checks a record's trigger before it is written: exactly one of cron
// or one-off must be set, a cron expression must parse, and a named timezone
// must resolve. It does not check the workflow ref (the caller validates that
// against the registry).
func Validate(rec Schedule) error {
	hasCron := rec.Trigger.Cron != ""
	hasAt := !rec.Trigger.At.IsZero()
	switch {
	case hasCron && hasAt:
		return fmt.Errorf("schedule: trigger has both cron and one-off time; set exactly one")
	case !hasCron && !hasAt:
		return fmt.Errorf("schedule: trigger has neither cron nor one-off time; set exactly one")
	}
	if hasCron && !gronx.IsValid(rec.Trigger.Cron) {
		return fmt.Errorf("schedule: invalid cron expression %q", rec.Trigger.Cron)
	}
	if rec.Trigger.TZ != "" {
		if _, err := time.LoadLocation(rec.Trigger.TZ); err != nil {
			return fmt.Errorf("schedule: invalid timezone %q: %w", rec.Trigger.TZ, err)
		}
	}
	return nil
}

// NewSchedule builds a Schedule with the trigger-independent defaults shared by
// every schedule creation path: WorkflowID, Ref, Path, Params, and Enabled=true.
// The caller sets Trigger (and, for inline records, ID; for cron records,
// Overlap) before persisting. path must already be absolute; the caller resolves
// it via filepath.Abs.
func NewSchedule(workflowID, ref, path string, params map[string]string) Schedule {
	return Schedule{
		WorkflowID: workflowID,
		Ref:        ref,
		Path:       path,
		Params:     params,
		Enabled:    true,
	}
}

// NewCronSchedule builds a fully-initialized Schedule for a recurring cron
// schedule. It extends [NewSchedule] by setting Trigger and Overlap so the
// caller does not mutate fields after construction. path must already be
// absolute.
func NewCronSchedule(workflowID, ref, path string, params map[string]string, trigger Trigger, overlap OverlapPolicy) Schedule {
	rec := NewSchedule(workflowID, ref, path, params)
	rec.Trigger = trigger
	rec.Overlap = overlap
	return rec
}

// NewAtSchedule builds a fully-initialized Schedule for a one-off schedule. It
// extends [NewSchedule] by setting Trigger so the caller does not mutate fields
// after construction. path must already be absolute.
func NewAtSchedule(workflowID, ref, path string, params map[string]string, trigger Trigger) Schedule {
	rec := NewSchedule(workflowID, ref, path, params)
	rec.Trigger = trigger
	return rec
}

// Add validates rec, assigns its id (when empty), CreatedAt, and initial
// NextRunAt, then writes it atomically under root. It returns the stored record.
func Add(root string, rec Schedule, cfg Config) (Schedule, error) {
	if err := Validate(rec); err != nil {
		return Schedule{}, err
	}
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	randFn := defaultRandSuffix
	if cfg.Rand != nil {
		randFn = cfg.Rand
	}
	rec.CreatedAt = now().UTC()
	if rec.ID == "" {
		suffix, err := randFn()
		if err != nil {
			return Schedule{}, fmt.Errorf("schedule: generate id: %w", err)
		}
		rec.ID = rec.WorkflowID + "_" + triggerKind(rec.Trigger) + "_" + suffix
	}
	next, err := rec.NextRunAfter(rec.CreatedAt)
	if err != nil {
		return Schedule{}, err
	}
	rec.NextRunAt = next
	if err := write(root, rec); err != nil {
		return Schedule{}, err
	}
	return rec, nil
}

// Update rewrites an existing schedule by id. The record must already exist;
// updating a missing id is an error so a typo does not silently create a record.
func Update(root string, rec Schedule) error {
	if rec.ID == "" {
		return fmt.Errorf("schedule: update requires a record id")
	}
	if _, err := os.Stat(recordPath(root, rec.ID)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("schedule: %q not found", rec.ID)
		}
		return fmt.Errorf("schedule: stat %q: %w", rec.ID, err)
	}
	return write(root, rec)
}

// Get reads a single schedule by id.
func Get(root, id string) (Schedule, error) {
	data, err := os.ReadFile(recordPath(root, id))
	if err != nil {
		if os.IsNotExist(err) {
			return Schedule{}, fmt.Errorf("schedule: %q not found", id)
		}
		return Schedule{}, fmt.Errorf("schedule: read %q: %w", id, err)
	}
	var rec Schedule
	if err := json.Unmarshal(data, &rec); err != nil {
		return Schedule{}, fmt.Errorf("schedule: parse %q: %w", id, err)
	}
	return rec, nil
}

// Remove deletes a schedule by id. A missing schedule is an error so the CLI
// can report a clear "not found" rather than a silent no-op.
func Remove(root, id string) error {
	if err := os.Remove(recordPath(root, id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("schedule: %q not found", id)
		}
		return fmt.Errorf("schedule: remove %q: %w", id, err)
	}
	return nil
}

// List returns every schedule under root, newest CreatedAt first. When
// workflowFilter is non-empty only schedules for that workflow id are returned.
// A missing schedules directory is not an error: it returns an empty slice so a
// fresh install lists cleanly.
func List(root, workflowFilter string) ([]Schedule, error) {
	dir := schedulesDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("schedule: read dir %s: %w", dir, err)
	}
	var recs []Schedule
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("schedule: read %s: %w", name, err)
		}
		var rec Schedule
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("schedule: parse %s: %w", name, err)
		}
		if workflowFilter != "" && rec.WorkflowID != workflowFilter {
			continue
		}
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].ID > recs[j].ID
		}
		return recs[i].CreatedAt.After(recs[j].CreatedAt)
	})
	return recs, nil
}

// inlineIDSuffix marks schedule IDs that originate from a workflow's inline
// `schedule:` block. It is the single authority for the naming convention so
// a re-sync upserts the same record and a dropped block can find and remove
// its prior record.
const inlineIDSuffix = "_inline"

// SyncInline upserts or removes the inline schedule for wf and returns a
// SyncResult describing the change. path must be the absolute workflow file
// path. ref is the display name recorded on the schedule record and shown in
// messages.
//
// Field-preservation rule on update: CreatedAt, LastRunAt, LastRunID, and
// Enabled from the existing record survive the re-sync; NextRunAt is
// recomputed from the (possibly updated) cron expression.
func SyncInline(home string, wf *workflow.Workflow, path, ref string) (SyncResult, error) {
	id := string(wf.ID) + inlineIDSuffix
	existing, getErr := Get(home, id)

	if wf.Schedule == nil {
		if getErr == nil {
			if err := Remove(home, id); err != nil {
				return SyncResult{}, err
			}
			return SyncResult{Action: SyncRemoved, ID: id}, nil
		}
		return SyncResult{Action: SyncNoOp}, nil
	}

	rec := NewSchedule(string(wf.ID), ref, path, nil)
	rec.ID = id
	rec.Trigger = Trigger{Cron: wf.Schedule.Cron, TZ: wf.Schedule.TZ}
	if err := Validate(rec); err != nil {
		return SyncResult{}, err
	}
	if getErr == nil {
		rec.CreatedAt = existing.CreatedAt
		rec.LastRunAt = existing.LastRunAt
		rec.LastRunID = existing.LastRunID
		rec.Enabled = existing.Enabled
		next, err := rec.NextRunAfter(time.Now())
		if err != nil {
			return SyncResult{}, err
		}
		rec.NextRunAt = next
		if err := Update(home, rec); err != nil {
			return SyncResult{}, err
		}
		return SyncResult{Action: SyncUpdated, ID: id, NextRunAt: rec.NextRunAt}, nil
	}
	stored, err := Add(home, rec, Config{})
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Action: SyncAdded, ID: stored.ID, NextRunAt: stored.NextRunAt}, nil
}

// SchedulesDir returns the directory under root where schedule records are
// stored. It is the single authority for the layout; daemon helpers call this
// instead of re-encoding the path independently.
func SchedulesDir(root string) string {
	return schedulesDir(root)
}

// write atomically persists rec to <root>/schedules/<id>.json.
func write(root string, rec Schedule) error {
	dir := schedulesDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("schedule: create dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("schedule: marshal %q: %w", rec.ID, err)
	}
	data = append(data, '\n')
	path := recordPath(root, rec.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("schedule: write %q: %w", rec.ID, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("schedule: rename %q: %w", rec.ID, err)
	}
	return nil
}

func schedulesDir(root string) string {
	if root == "" {
		root = ".loom"
	}
	return filepath.Join(root, "schedules")
}

func recordPath(root, id string) string {
	return filepath.Join(schedulesDir(root), id+".json")
}

func triggerKind(t Trigger) string {
	if t.IsCron() {
		return "cron"
	}
	return "at"
}

func defaultRandSuffix() (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
