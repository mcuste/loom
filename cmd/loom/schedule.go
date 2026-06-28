package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/workflow"
)

// newScheduleCmd is the parent for managing workflow schedules. Its `cron` and
// `at` subcommands create schedules (recurring and one-off); `ls`, `rm`,
// `enable`, and `disable` inspect and edit them. The records are read by
// `loom daemon`, which fires the runs.
func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage workflow schedules fired by `loom daemon`",
	}
	cmd.AddCommand(
		newScheduleCronCmd(),
		newScheduleAtCmd(),
		newScheduleListCmd(),
		newScheduleRemoveCmd(),
		newScheduleEnableCmd(),
		newScheduleDisableCmd(),
	)
	return cmd
}

func newScheduleCronCmd() *cobra.Command {
	var (
		paramArgs []string
		expr      string
		tz        string
		overlap   string
		catchup   bool
	)
	cmd := &cobra.Command{
		Use:               "cron <workflow>",
		Short:             "Schedule a workflow to run on a recurring cron expression",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleCron(cmd.OutOrStdout(), args[0], expr, tz, overlap, catchup, paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	cmd.Flags().StringVar(&expr, "expr", "", "cron expression, e.g. \"0 15 * * *\" (required)")
	cmd.Flags().StringVar(&tz, "tz", "", "IANA timezone the expression is evaluated in (default: daemon local time)")
	cmd.Flags().StringVar(&overlap, "overlap", "skip", "policy when a prior run is still in flight: skip|queue|allow")
	cmd.Flags().BoolVar(&catchup, "catchup", false, "fire once on daemon startup if a scheduled tick was missed")
	_ = cmd.MarkFlagRequired("expr")
	return cmd
}

func newScheduleAtCmd() *cobra.Command {
	var (
		paramArgs []string
		timeStr   string
		dateStr   string
		tz        string
		catchup   bool
	)
	cmd := &cobra.Command{
		Use:               "at <workflow>",
		Short:             "Schedule a workflow to run once at a given time",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleAt(cmd.OutOrStdout(), args[0], timeStr, dateStr, tz, catchup, paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	cmd.Flags().StringVar(&timeStr, "time", "", "clock time HH:MM (required)")
	cmd.Flags().StringVar(&dateStr, "date", "", "calendar date YYYY-MM-DD (default: today, or tomorrow if the time already passed)")
	cmd.Flags().StringVar(&tz, "tz", "", "IANA timezone the time is interpreted in (default: daemon local time)")
	cmd.Flags().BoolVar(&catchup, "catchup", false, "run even if the daemon was down when the instant passed")
	_ = cmd.MarkFlagRequired("time")
	return cmd
}

func newScheduleListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls [workflow]",
		Aliases: []string{"list"},
		Short:   "List schedules as a plain table",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleList(cmd.OutOrStdout(), firstArg(args))
		},
	}
	return cmd
}

func newScheduleRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"remove"},
		Short:   "Remove a schedule by id",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleRemove(cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

func newScheduleEnableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a disabled schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleToggle(cmd.OutOrStdout(), args[0], true)
		},
	}
	return cmd
}

func newScheduleDisableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a schedule without removing it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleToggle(cmd.OutOrStdout(), args[0], false)
		},
	}
	return cmd
}

// doScheduleCron validates the workflow and params, then persists a recurring
// schedule. Validation happens now so a bad workflow, missing required param,
// or malformed cron expression fails at the prompt, not at 15:00.
func doScheduleCron(w io.Writer, ref, expr, tz, overlap string, catchup bool, paramArgs []string) error {
	switch schedule.Overlap(overlap) {
	case schedule.OverlapSkip, schedule.OverlapQueue, schedule.OverlapAllow:
	default:
		return fmt.Errorf("invalid --overlap %q: want skip, queue, or allow", overlap)
	}
	wf, _, path, params, err := loadAndResolve(ref, paramArgs)
	if err != nil {
		return err
	}
	rec := schedule.Record{
		WorkflowID: string(wf.ID),
		Ref:        ref,
		Path:       absPath(path),
		Trigger:    schedule.Trigger{Cron: expr, TZ: tz},
		Params:     params,
		Enabled:    true,
		Overlap:    schedule.Overlap(overlap),
		Catchup:    catchup,
	}
	return addAndReport(w, rec)
}

// doScheduleAt validates the workflow and params, parses the one-off instant in
// the chosen timezone, and persists a one-off schedule.
func doScheduleAt(w io.Writer, ref, timeStr, dateStr, tz string, catchup bool, paramArgs []string) error {
	loc := time.Local
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return fmt.Errorf("invalid --tz %q: %w", tz, err)
		}
		loc = l
	}
	at, err := parseAtTime(timeStr, dateStr, loc, time.Now())
	if err != nil {
		return err
	}
	wf, _, path, params, err := loadAndResolve(ref, paramArgs)
	if err != nil {
		return err
	}
	rec := schedule.Record{
		WorkflowID: string(wf.ID),
		Ref:        ref,
		Path:       absPath(path),
		Trigger:    schedule.Trigger{At: at, TZ: tz},
		Params:     params,
		Enabled:    true,
		Catchup:    catchup,
	}
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
func loadAndResolve(ref string, paramArgs []string) (*workflow.Workflow, []byte, string, map[string]string, error) {
	wf, manifest, path, err := loadWorkflow(ref)
	if err != nil {
		return nil, nil, "", nil, err
	}
	cliParams, err := workflow.ParseParamArgs(paramArgs)
	if err != nil {
		return nil, nil, "", nil, err
	}
	if _, err := workflow.ResolveParams(wf, cliParams, nil); err != nil {
		return nil, nil, "", nil, err
	}
	if len(cliParams) == 0 {
		cliParams = nil
	}
	return wf, manifest, path, cliParams, nil
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
		stored.ID, triggerSummary(stored.Trigger), fireTime(stored.NextFire))
	return err
}

func doScheduleList(w io.Writer, workflowFilter string) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	recs, err := schedule.List(home, workflowFilter)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		_, err := fmt.Fprintln(w, "no schedules")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tWORKFLOW\tTRIGGER\tNEXT FIRE\tENABLED\tOVERLAP")
	for _, r := range recs {
		enabled := "yes"
		if !r.Enabled {
			enabled = "no"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.WorkflowID, triggerSummary(r.Trigger), fireTime(r.NextFire), enabled, r.EffectiveOverlap())
	}
	return tw.Flush()
}

func doScheduleRemove(w io.Writer, id string) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	if err := schedule.Remove(home, id); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "removed %s\n", id)
	return err
}

func doScheduleToggle(w io.Writer, id string, enabled bool) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	rec, err := schedule.Get(home, id)
	if err != nil {
		return err
	}
	rec.Enabled = enabled
	if err := schedule.Update(home, rec); err != nil {
		return err
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	_, err = fmt.Fprintf(w, "%s %s\n", state, id)
	return err
}

// triggerSummary renders a trigger for the ls table and confirmation lines.
func triggerSummary(t schedule.Trigger) string {
	if t.IsCron() {
		if t.TZ != "" {
			return fmt.Sprintf("cron %q %s", t.Cron, t.TZ)
		}
		return fmt.Sprintf("cron %q", t.Cron)
	}
	return "at " + fireTime(t.At)
}

// fireTime renders an instant for display, or "-" when unset.
func fireTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04 MST")
}
