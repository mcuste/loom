package schedule

import (
	"time"

	"github.com/mcuste/loom/pkg/workflow"
)

// inlineIDSuffix marks schedule IDs that originate from a workflow's inline
// `schedule:` block. It is the single authority for the naming convention so
// a re-sync upserts the same record and a dropped block can find and remove
// its prior record.
const inlineIDSuffix = "_inline"

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
	// NextFire is the next fire instant for SyncAdded and SyncUpdated.
	// Zero for SyncRemoved and SyncNoOp.
	NextFire time.Time
}

// SyncInline upserts or removes the inline schedule for wf and returns a
// SyncResult describing the change. path must be the absolute workflow file
// path. ref is the display name recorded on the schedule record and shown in
// messages.
//
// Field-preservation rule on update: CreatedAt, LastFire, LastRunID, and
// Enabled from the existing record survive the re-sync; NextFire is
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

	rec := NewRecord(string(wf.ID), ref, path, nil, false)
	rec.ID = id
	rec.Trigger = Trigger{Cron: wf.Schedule.Cron, TZ: wf.Schedule.TZ}
	if err := Validate(rec); err != nil {
		return SyncResult{}, err
	}
	if getErr == nil {
		rec.CreatedAt = existing.CreatedAt
		rec.LastFire = existing.LastFire
		rec.LastRunID = existing.LastRunID
		rec.Enabled = existing.Enabled
		next, err := rec.NextFireAfter(time.Now())
		if err != nil {
			return SyncResult{}, err
		}
		rec.NextFire = next
		if err := Update(home, rec); err != nil {
			return SyncResult{}, err
		}
		return SyncResult{Action: SyncUpdated, ID: id, NextFire: rec.NextFire}, nil
	}
	stored, err := Add(home, rec, Config{})
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Action: SyncAdded, ID: stored.ID, NextFire: stored.NextFire}, nil
}
