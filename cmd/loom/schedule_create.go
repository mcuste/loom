package main

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/spf13/cobra"
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
// policy, and the repeatable -p params. Embedded into cronOpts and atOpts;
// addTriggerFlags binds -p / --tz / --catchup once for both builders.
type triggerCommon struct {
	tz        string
	catchup   bool
	paramArgs []string
}

// addTriggerFlags binds the flags shared by every trigger builder: the
// repeatable -p params and the --tz / --catchup pair. Each builder passes its
// own help strings so cron and at can describe their semantics precisely.
func addTriggerFlags(cmd *cobra.Command, c *triggerCommon, tzHelp, catchupHelp string) {
	addParamFlags(cmd, &c.paramArgs)
	cmd.Flags().StringVar(&c.tz, "tz", "", tzHelp)
	cmd.Flags().BoolVar(&c.catchup, "catchup", false, catchupHelp)
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
	rec := schedule.NewCronRecord(string(wf.ID), ref, absPath(path), params, o.catchup,
		schedule.Trigger{Cron: o.expr, TZ: o.tz}, overlap)
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
	rec := schedule.NewAtRecord(string(wf.ID), ref, absPath(path), params, o.catchup,
		schedule.Trigger{At: at, TZ: o.tz})
	return addAndReport(w, rec)
}

// parseAtTime passes --time and --date as field labels to schedule.ParseAtTime
// so format errors name the offending flag rather than a generic field name.
func parseAtTime(timeStr, dateStr string, loc *time.Location, now time.Time) (time.Time, error) {
	return schedule.ParseAtTime(timeStr, dateStr, loc, now, "--time", "--date")
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
		stored.ID, stored.Trigger.Summary(), tui.FormatFireTime(stored.NextFire))
	return err
}
