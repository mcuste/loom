package main

import (
	"github.com/spf13/cobra"
)

// newScheduleCmd is the parent for managing workflow schedules. Its `cron` and
// `at` subcommands create schedules (recurring and one-off); `ls`, `rm`,
// `enable`, and `disable` inspect and edit them. The records are read by
// `loom daemon`, which fires the runs.
func newScheduleCmd(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage workflow schedules fired by `loom daemon`",
	}
	cmd.AddCommand(
		newScheduleCronCmd(env),
		newScheduleAtCmd(env),
		newScheduleListCmd(env),
		newScheduleRemoveCmd(env),
		newScheduleToggleCmd(env, "enable", "Enable a disabled schedule", true),
		newScheduleToggleCmd(env, "disable", "Disable a schedule without removing it", false),
		newScheduleSyncCmd(env),
	)
	return cmd
}

func newScheduleCronCmd(env *cliEnv) *cobra.Command {
	var o cronOpts
	cmd := &cobra.Command{
		Use:               "cron <workflow>",
		Short:             "Schedule a workflow to run on a recurring cron expression",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleCron(cmd.OutOrStdout(), env.home, env.cwd, args[0], o)
		},
	}
	addTriggerFlags(cmd, &o.triggerCommon,
		"IANA timezone the expression is evaluated in (default: daemon local time)",
		"fire once on daemon startup if a scheduled tick was missed")
	cmd.Flags().StringVar(&o.expr, "expr", "", "cron expression, e.g. \"0 15 * * *\" (required)")
	cmd.Flags().StringVar(&o.overlap, "overlap", "skip", "policy when a prior run is still in flight: skip|queue|allow")
	_ = cmd.MarkFlagRequired("expr")
	return cmd
}

func newScheduleAtCmd(env *cliEnv) *cobra.Command {
	var o atOpts
	cmd := &cobra.Command{
		Use:               "at <workflow>",
		Short:             "Schedule a workflow to run once at a given time",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleAt(cmd.OutOrStdout(), env.home, env.cwd, args[0], o)
		},
	}
	addTriggerFlags(cmd, &o.triggerCommon,
		"IANA timezone the time is interpreted in (default: daemon local time)",
		"run even if the daemon was down when the instant passed")
	cmd.Flags().StringVar(&o.timeStr, "time", "", "clock time HH:MM (required)")
	cmd.Flags().StringVar(&o.dateStr, "date", "", "calendar date YYYY-MM-DD (default: today, or tomorrow if the time already passed)")
	_ = cmd.MarkFlagRequired("time")
	return cmd
}

func newScheduleListCmd(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls [workflow]",
		Aliases: []string{"list"},
		Short:   "List schedules as a plain table",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleList(cmd.OutOrStdout(), env.home, firstArg(args))
		},
	}
	return cmd
}

func newScheduleRemoveCmd(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"remove"},
		Short:   "Remove a schedule by id",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleRemove(cmd.OutOrStdout(), env.home, args[0])
		},
	}
	return cmd
}

// newScheduleToggleCmd builds the `enable`/`disable` pair: identical save for the
// verb and the enabled bit they flip, so one factory serves both.
func newScheduleToggleCmd(env *cliEnv, use, short string, enabled bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doScheduleToggle(cmd.OutOrStdout(), env.home, args[0], enabled)
		},
	}
}

func newScheduleSyncCmd(env *cliEnv) *cobra.Command {
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
			return doScheduleSync(cmd.OutOrStdout(), env.home, env.cwd, firstArg(args))
		},
	}
	return cmd
}
