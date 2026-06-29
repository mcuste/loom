package main

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/workflow"
)

// absPath returns the absolute form of p, falling back to p when resolution
// fails. The scheduler stores an absolute workflow path so the daemon reloads
// the same file regardless of its own working directory.
func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// triggerCommon holds the schedule flags shared by `schedule cron` and
// `schedule at`: the timezone the trigger is interpreted in, the catch-up
// policy, and the repeatable -p params. Embedded into cronOpts and atOpts so the
// clump is declared (and flag-bound) once.
type triggerCommon struct {
	tz        string
	catchup   bool
	paramArgs []string
}

// cronOpts bundles the trigger-shaping flags of `schedule cron` so the handler
// takes the clump as one unit rather than a long positional list.
type cronOpts struct {
	triggerCommon
	expr    string
	overlap string
}

// atOpts bundles the trigger-shaping flags of `schedule at` so the handler
// takes the clump as one unit rather than a long positional list.
type atOpts struct {
	triggerCommon
	timeStr string
	dateStr string
}

// baseRecord builds the trigger-independent fields shared by `schedule cron`,
// `schedule at`, and `schedule sync` (inline blocks). The caller sets Trigger
// (and, for cron, Overlap; for inline, ID) on the returned record before
// persisting it.
func baseRecord(wf *workflow.Workflow, ref, path string, params map[string]string, catchup bool) schedule.Record {
	return schedule.Record{
		WorkflowID: string(wf.ID),
		Ref:        ref,
		Path:       absPath(path),
		Params:     params,
		Enabled:    true,
		Catchup:    catchup,
	}
}

// doScheduleCron validates the workflow and params, then persists a recurring
// schedule. Validation happens now so a bad workflow, missing required param,
// or malformed cron expression fails at the prompt, not at 15:00.
func doScheduleCron(w io.Writer, ref string, o cronOpts) error {
	overlap, err := schedule.ParseOverlap(o.overlap)
	if err != nil {
		return err
	}
	wf, path, params, err := loadAndResolve(ref, o.paramArgs)
	if err != nil {
		return err
	}
	rec := baseRecord(wf, ref, path, params, o.catchup)
	rec.Trigger = schedule.Trigger{Cron: o.expr, TZ: o.tz}
	rec.Overlap = overlap
	return addAndReport(w, rec)
}

// doScheduleAt validates the workflow and params, parses the one-off instant in
// the chosen timezone, and persists a one-off schedule.
func doScheduleAt(w io.Writer, ref string, o atOpts) error {
	loc := time.Local
	if o.tz != "" {
		l, err := time.LoadLocation(o.tz)
		if err != nil {
			return fmt.Errorf("invalid --tz %q: %w", o.tz, err)
		}
		loc = l
	}
	at, err := parseAtTime(o.timeStr, o.dateStr, loc, time.Now())
	if err != nil {
		return err
	}
	wf, path, params, err := loadAndResolve(ref, o.paramArgs)
	if err != nil {
		return err
	}
	rec := baseRecord(wf, ref, path, params, o.catchup)
	rec.Trigger = schedule.Trigger{At: at, TZ: o.tz}
	return addAndReport(w, rec)
}

// parseAtTime turns a clock time (and optional date) in loc into a concrete
// instant. Without a date it uses today; if that instant has already passed it
// rolls to the next day, so "at 15:00" means the next 15:00. A supplied date is
// honored verbatim (no rollover) so an explicit past date surfaces as such.
func parseAtTime(timeStr, dateStr string, loc *time.Location, now time.Time) (time.Time, error) {
	hm, err := time.Parse("15:04", timeStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --time %q: want HH:MM", timeStr)
	}
	if dateStr != "" {
		d, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid --date %q: want YYYY-MM-DD", dateStr)
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

// loadAndResolve loads the workflow and resolves its params, returning the
// CLI-supplied param map (not the defaults) so the daemon resolves fresh
// against the then-current workflow at fire time. ResolveParams is still called
// here to reject a missing required param up front.
func loadAndResolve(ref string, paramArgs []string) (*workflow.Workflow, string, map[string]string, error) {
	wf, _, path, err := loadWorkflow(ref)
	if err != nil {
		return nil, "", nil, err
	}
	cliParams, err := workflow.ParseParamArgs(paramArgs)
	if err != nil {
		return nil, "", nil, err
	}
	if _, err := workflow.ResolveParams(wf, cliParams, nil); err != nil {
		return nil, "", nil, err
	}
	if len(cliParams) == 0 {
		cliParams = nil
	}
	return wf, path, cliParams, nil
}

func addAndReport(w io.Writer, rec schedule.Record) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	stored, err := schedule.Add(home, rec, schedule.Config{})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "scheduled %s (%s), next fire %s\n",
		stored.ID, triggerSummary(stored.Trigger), formatFireTime(stored.NextFire))
	return err
}
