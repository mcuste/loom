// Package schedule persists workflow schedules to disk so the loom daemon can
// fire runs at cron times or one-off instants without the user keeping a shell
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
// instant), the parameters to pass, and the overlap/catch-up policy. Files are
// rewritten atomically (write to .tmp, rename) so a crash mid-write never
// leaves a half-written schedule on disk, mirroring the run store in
// [github.com/mcuste/loom/pkg/store].
//
// The package owns storage, validation, and next-fire computation; the firing
// policy (overlap handling, catch-up on startup) lives in the daemon.
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
)

// Overlap is the policy for a fire that arrives while the schedule's previous
// run is still in flight.
type Overlap string

const (
	// OverlapSkip drops the new fire if the previous run is still running. The
	// default: safest for workflows that carry cross-run state.
	OverlapSkip Overlap = "skip"
	// OverlapQueue serializes fires per schedule: the new fire waits for the
	// previous run to finish, then runs.
	OverlapQueue Overlap = "queue"
	// OverlapAllow fires concurrently regardless of any in-flight run.
	OverlapAllow Overlap = "allow"
)

// Trigger is a schedule's timing rule. Exactly one of Cron or At is set: Cron
// is a recurring gronx expression, At is a one-off fire instant (stored UTC).
// TZ is the IANA location name the cron expression is evaluated in; empty means
// the daemon's local time.
type Trigger struct {
	Cron string    `json:"cron,omitempty"`
	At   time.Time `json:"at,omitzero"`
	TZ   string    `json:"tz,omitempty"`
}

// IsCron reports whether the trigger is a recurring cron rule.
func (t Trigger) IsCron() bool { return t.Cron != "" }

// Record is a single persisted schedule.
type Record struct {
	// ID is the stable on-disk identity ("<workflow_id>_<kind>_<suffix>").
	ID string `json:"id"`
	// WorkflowID is the workflow's id, kept for display and the `ls` filter.
	WorkflowID string `json:"workflow_id"`
	// Ref is the original CLI workflow argument (a registry name or a path).
	Ref string `json:"ref"`
	// Path is the resolved absolute YAML path the daemon loads at fire time, so
	// a cwd-relative registry lookup is not repeated from the daemon's cwd.
	Path string `json:"path"`
	// Trigger is the timing rule (cron or one-off).
	Trigger Trigger `json:"trigger"`
	// Params holds the resolved -p key=val values to pass to each run.
	Params map[string]string `json:"params,omitempty"`
	// Enabled gates firing: a disabled schedule is retained but never fired.
	Enabled bool `json:"enabled"`
	// Overlap is the in-flight-run policy; empty means OverlapSkip.
	Overlap Overlap `json:"overlap,omitempty"`
	// Catchup, when true, fires a cron schedule once on daemon startup if its
	// previous fire was missed (the daemon was down), and runs a past-due
	// one-off rather than dropping it.
	Catchup bool `json:"catchup,omitempty"`
	// NextFire is the next instant this schedule is due (UTC). Maintained by the
	// daemon; Add seeds it from the trigger.
	NextFire time.Time `json:"next_fire,omitzero"`
	// LastFire is the instant the schedule last fired (UTC). Zero until first
	// fired.
	LastFire time.Time `json:"last_fire,omitzero"`
	// LastRunID is the run id of the most recent fire, linking the schedule to a
	// record in the run store.
	LastRunID string `json:"last_run_id,omitempty"`
	// CreatedAt is when the schedule was added (UTC).
	CreatedAt time.Time `json:"created_at"`
}

// EffectiveOverlap returns the record's overlap policy, defaulting an empty
// value to OverlapSkip.
func (r Record) EffectiveOverlap() Overlap {
	if r.Overlap == "" {
		return OverlapSkip
	}
	return r.Overlap
}

// Config is the optional configuration for [Add]. The zero value is valid.
type Config struct {
	// Now overrides the clock used for CreatedAt and the initial NextFire; nil
	// uses time.Now. Tests inject a fixed clock for determinism.
	Now func() time.Time
	// Rand returns the random suffix appended to a generated id. nil uses
	// crypto/rand; tests inject a deterministic source.
	Rand func() (string, error)
}

// Validate checks a record's trigger before it is written: exactly one of cron
// or one-off must be set, a cron expression must parse, and a named timezone
// must resolve. It does not check the workflow ref (the caller validates that
// against the registry).
func Validate(rec Record) error {
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

// Add validates rec, assigns its id (when empty), CreatedAt, and initial
// NextFire, then writes it atomically under root. It returns the stored record.
func Add(root string, rec Record, cfg Config) (Record, error) {
	if err := Validate(rec); err != nil {
		return Record{}, err
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
			return Record{}, fmt.Errorf("schedule: generate id: %w", err)
		}
		rec.ID = rec.WorkflowID + "_" + triggerKind(rec.Trigger) + "_" + suffix
	}
	next, err := rec.NextFireAfter(rec.CreatedAt)
	if err != nil {
		return Record{}, err
	}
	rec.NextFire = next
	if err := write(root, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Update rewrites an existing schedule by id. The record must already exist;
// updating a missing id is an error so a typo does not silently create a record.
func Update(root string, rec Record) error {
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
func Get(root, id string) (Record, error) {
	data, err := os.ReadFile(recordPath(root, id))
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, fmt.Errorf("schedule: %q not found", id)
		}
		return Record{}, fmt.Errorf("schedule: read %q: %w", id, err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("schedule: parse %q: %w", id, err)
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
func List(root, workflowFilter string) ([]Record, error) {
	dir := schedulesDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("schedule: read dir %s: %w", dir, err)
	}
	var recs []Record
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("schedule: read %s: %w", name, err)
		}
		var rec Record
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

// write atomically persists rec to <root>/schedules/<id>.json.
func write(root string, rec Record) error {
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
