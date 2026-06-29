package main

import (
	"fmt"
	"io"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/workflow"
)

// inlineIDSuffix marks schedule ids that originate from a workflow's inline
// `schedule:` block, so a re-sync upserts the same record rather than spawning
// duplicates and so a dropped block can find and remove its prior record.
const inlineIDSuffix = "_inline"

// doScheduleSync reconciles inline schedules. With a workflow argument it syncs
// that one; with none it walks the registry, skipping (with a note) any
// workflow that fails to load so one broken file does not abort the sweep.
func doScheduleSync(w io.Writer, ref string) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	if ref == "" {
		return syncAll(w, home)
	}
	msg, err := syncOne(home, ref, ref)
	if err != nil {
		return err
	}
	if msg == "" {
		msg = "no inline schedule"
	}
	_, err = fmt.Fprintf(w, "%s: %s\n", ref, msg)
	return err
}

// syncAll reconciles the inline schedule of every registry workflow, skipping
// (with a note) any workflow that fails to load so one broken file does not
// abort the sweep. It reports when nothing had an inline block to sync.
func syncAll(w io.Writer, home string) error {
	refs, err := listRegistryWorkflows()
	if err != nil {
		return err
	}
	synced := 0
	for _, r := range refs {
		msg, err := syncOne(home, r.path, r.name)
		if err != nil {
			_, _ = fmt.Fprintf(w, "skip %s: %v\n", r.name, err)
			continue
		}
		if msg != "" {
			_, _ = fmt.Fprintf(w, "%s: %s\n", r.name, msg)
			synced++
		}
	}
	if synced == 0 {
		_, err = fmt.Fprintln(w, "no inline schedules found")
		return err
	}
	return nil
}

// syncOne loads the workflow at loadRef (a registry name or a file path) and
// reconciles its inline schedule, returning syncInlineSchedule's status ("" when
// the workflow carries no inline block). displayName is the ref recorded on the
// schedule and shown in messages, which differs from loadRef in the sweep (a
// path loads, the colon-name displays).
func syncOne(home, loadRef, displayName string) (string, error) {
	wf, _, path, err := loadWorkflow(loadRef)
	if err != nil {
		return "", err
	}
	return syncInlineSchedule(home, wf, absPath(path), displayName)
}

// syncInlineSchedule upserts (or removes) the inline schedule for one workflow
// and returns a human-readable status, or "" when there is nothing to do. An
// existing record's CreatedAt, last-fire bookkeeping, and manual enabled state
// are preserved across the update.
func syncInlineSchedule(home string, wf *workflow.Workflow, path, ref string) (string, error) {
	id := string(wf.ID) + inlineIDSuffix
	existing, getErr := schedule.Get(home, id)

	if wf.Schedule == nil {
		if getErr == nil {
			if err := schedule.Remove(home, id); err != nil {
				return "", err
			}
			return "removed inline schedule (block dropped)", nil
		}
		return "", nil
	}

	rec := baseRecord(wf, ref, path, nil, false)
	rec.ID = id
	rec.Trigger = schedule.Trigger{Cron: wf.Schedule.Cron, TZ: wf.Schedule.TZ}
	if err := schedule.Validate(rec); err != nil {
		return "", err
	}
	if getErr == nil {
		rec.CreatedAt = existing.CreatedAt
		rec.LastFire = existing.LastFire
		rec.LastRunID = existing.LastRunID
		rec.Enabled = existing.Enabled
		next, err := rec.NextFireAfter(time.Now())
		if err != nil {
			return "", err
		}
		rec.NextFire = next
		if err := schedule.Update(home, rec); err != nil {
			return "", err
		}
		return fmt.Sprintf("updated inline schedule %s", id), nil
	}
	stored, err := schedule.Add(home, rec, schedule.Config{})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("added inline schedule %s, next fire %s", stored.ID, formatFireTime(stored.NextFire)), nil
}
