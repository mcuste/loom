package main

import (
	"fmt"
	"io"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/mcuste/loom/pkg/workflowload"
	"github.com/spf13/cobra"
)

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
func doScheduleCron(w io.Writer, home, cwd, ref string, o cronOpts) error {
	overlap, err := schedule.ParseOverlap(o.overlap)
	if err != nil {
		return err
	}
	wf, path, params, err := loadAndResolve(home, cwd, ref, o.paramArgs)
	if err != nil {
		return err
	}
	rec := schedule.NewCronRecord(string(wf.ID), ref, path, params, o.catchup,
		schedule.Trigger{Cron: o.expr, TZ: o.tz}, overlap)
	return addAndReport(w, home, rec)
}

// doScheduleAt validates the workflow and params, parses the one-off instant in
// the chosen timezone, and persists a one-off schedule.
func doScheduleAt(w io.Writer, home, cwd, ref string, o atOpts) error {
	loc := time.Local
	if o.tz != "" {
		l, err := time.LoadLocation(o.tz)
		if err != nil {
			return fmt.Errorf("invalid --tz %q: %w", o.tz, err)
		}
		loc = l
	}
	at, err := schedule.ParseAtTime(o.timeStr, o.dateStr, loc, time.Now(), "--time", "--date")
	if err != nil {
		return err
	}
	wf, path, params, err := loadAndResolve(home, cwd, ref, o.paramArgs)
	if err != nil {
		return err
	}
	rec := schedule.NewAtRecord(string(wf.ID), ref, path, params, o.catchup,
		schedule.Trigger{At: at, TZ: o.tz})
	return addAndReport(w, home, rec)
}

// loadAndResolve loads the workflow and resolves its params, returning the
// CLI-supplied param map (not the defaults) so the daemon resolves fresh
// against the then-current workflow at fire time. ResolveAndValidateParams is
// still called here to reject missing required params and bad routing up front.
func loadAndResolve(home, cwd, ref string, paramArgs []string) (*workflow.Workflow, string, map[string]string, error) {
	wf, _, path, err := workflowload.Load(home, cwd, ref)
	if err != nil {
		return nil, "", nil, err
	}
	cliParams, err := workflow.ParseParamArgs(paramArgs)
	if err != nil {
		return nil, "", nil, err
	}
	if _, err := workflow.ResolveAndValidateParams(wf, cliParams, nil); err != nil {
		return nil, "", nil, err
	}
	if len(cliParams) == 0 {
		cliParams = nil
	}
	return wf, path, cliParams, nil
}

func addAndReport(w io.Writer, home string, rec schedule.Record) error {
	stored, err := schedule.Add(home, rec, schedule.Config{})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "scheduled %s (%s), next fire %s\n",
		stored.ID, stored.Trigger.Summary(), tui.FormatFireTime(stored.NextFire))
	return err
}
