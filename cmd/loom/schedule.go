package main

import (
	"github.com/spf13/cobra"
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
		newScheduleToggleCmd("enable", "Enable a disabled schedule", true),
		newScheduleToggleCmd("disable", "Disable a schedule without removing it", false),
		newScheduleSyncCmd(),
	)
	return cmd
}

func newScheduleCronCmd() *cobra.Command {
	var o cronOpts
	cmd := &cobra.Command{
		Use:               "cron <workflow>",
		Short:             "Schedule a workflow to run on a recurring cron expression",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleCron(cmd.OutOrStdout(), args[0], o)
		},
	}
	addParamFlags(cmd, &o.paramArgs)
	cmd.Flags().StringVar(&o.expr, "expr", "", "cron expression, e.g. \"0 15 * * *\" (required)")
	cmd.Flags().StringVar(&o.tz, "tz", "", "IANA timezone the expression is evaluated in (default: daemon local time)")
	cmd.Flags().StringVar(&o.overlap, "overlap", "skip", "policy when a prior run is still in flight: skip|queue|allow")
	cmd.Flags().BoolVar(&o.catchup, "catchup", false, "fire once on daemon startup if a scheduled tick was missed")
	_ = cmd.MarkFlagRequired("expr")
	return cmd
}

func newScheduleAtCmd() *cobra.Command {
	var o atOpts
	cmd := &cobra.Command{
		Use:               "at <workflow>",
		Short:             "Schedule a workflow to run once at a given time",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleAt(cmd.OutOrStdout(), args[0], o)
		},
	}
	addParamFlags(cmd, &o.paramArgs)
	cmd.Flags().StringVar(&o.timeStr, "time", "", "clock time HH:MM (required)")
	cmd.Flags().StringVar(&o.dateStr, "date", "", "calendar date YYYY-MM-DD (default: today, or tomorrow if the time already passed)")
	cmd.Flags().StringVar(&o.tz, "tz", "", "IANA timezone the time is interpreted in (default: daemon local time)")
	cmd.Flags().BoolVar(&o.catchup, "catchup", false, "run even if the daemon was down when the instant passed")
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

// newScheduleToggleCmd builds the `enable`/`disable` pair: identical save for the
// verb and the enabled bit they flip, so one factory serves both.
func newScheduleToggleCmd(use, short string, enabled bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleToggle(cmd.OutOrStdout(), args[0], enabled)
		},
	}
}

func newScheduleSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync [workflow]",
		Short: "Reconcile inline workflow `schedule:` blocks into the schedule store",
		Long: "Reconcile the inline `schedule:` block of one workflow (or every " +
			"registry workflow when no argument is given) into the schedule store. " +
			"A workflow that dropped its block has its synced schedule removed; a " +
			"schedule disabled by hand stays disabled across re-syncs.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleSync(cmd.OutOrStdout(), firstArg(args))
		},
	}
	return cmd
}
