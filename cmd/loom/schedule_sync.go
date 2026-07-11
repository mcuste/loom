package main

import (
	"fmt"
	"io"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflowload"
)

// doScheduleSync reconciles inline schedules. With a workflow argument it syncs
// that one; with none it walks the registry, skipping (with a note) any
// workflow that fails to load so one broken file does not abort the sweep.
func doScheduleSync(w io.Writer, home, cwd, ref string) error {
	if ref == "" {
		return syncAll(w, home, cwd)
	}
	msg, err := syncOne(home, cwd, ref, ref)
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
func syncAll(w io.Writer, home, cwd string) error {
	refs, err := workflowload.List(home, cwd)
	if err != nil {
		return err
	}
	synced := 0
	for _, r := range refs {
		msg, err := syncOne(home, cwd, r.Path, r.Name)
		if err != nil {
			_, _ = fmt.Fprintf(w, "skip %s: %v\n", r.Name, err)
			continue
		}
		if msg != "" {
			_, _ = fmt.Fprintf(w, "%s: %s\n", r.Name, msg)
			synced++
		}
	}
	if synced == 0 {
		_, err = fmt.Fprintln(w, "no inline schedules found")
		return err
	}
	return nil
}

// syncOne loads the workflow at loadRef (a registry name or a file path),
// delegates to schedule.SyncInline, and formats the result as a
// human-readable status string ("" when the workflow carries no inline block).
// displayName is the ref recorded on the schedule and shown in messages, which
// differs from loadRef in the sweep (a path loads, the colon-name displays).
func syncOne(home, cwd, loadRef, displayName string) (string, error) {
	wf, _, path, err := workflowload.Load(home, cwd, loadRef)
	if err != nil {
		return "", err
	}
	res, err := schedule.SyncInline(home, wf, path, displayName)
	if err != nil {
		return "", err
	}
	switch res.Action {
	case schedule.SyncAdded:
		loc, err := (schedule.Trigger{TZ: wf.Schedule.TZ}).Location()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("added inline schedule %s, next run %s", res.ID, tui.FormatScheduledTime(res.NextRunAt, loc)), nil
	case schedule.SyncUpdated:
		return fmt.Sprintf("updated inline schedule %s", res.ID), nil
	case schedule.SyncRemoved:
		return "removed inline schedule (block dropped)", nil
	default: // SyncNoOp
		return "", nil
	}
}
