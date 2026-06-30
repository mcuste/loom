package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/internal/scheduler"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the scheduler loop that fires scheduled workflows",
		Long: "Run the scheduler loop in the foreground. It reads schedules from " +
			"$LOOM_HOME/schedules and fires each workflow at its cron time or one-off " +
			"instant, recording every run in the normal run store. Use a process " +
			"supervisor (launchd/systemd) to keep it alive across reboots.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := loomHome()
			if err != nil {
				return err
			}
			ctx, stop := interruptContext()
			defer stop()
			d := scheduler.New(home, cmd.OutOrStdout(), loadWorkflow)
			return d.Run(ctx)
		},
	}
	cmd.AddCommand(newDaemonInstallCmd())
	return cmd
}

// newDaemonInstallCmd writes a platform supervisor unit (launchd on macOS,
// systemd on Linux) that keeps `loom daemon` running across logout and reboot,
// and by default loads it so the daemon starts immediately. Pass --manual to
// only write the unit and print the commands to enable it yourself.
func newDaemonInstallCmd() *cobra.Command {
	var manual bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and enable a launchd/systemd unit that keeps `loom daemon` running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			exec, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve loom binary path: %w", err)
			}
			home, err := loomHome()
			if err != nil {
				return err
			}
			return installDaemon(cmd.OutOrStdout(), exec, home, manual)
		},
	}
	cmd.Flags().BoolVar(&manual, "manual", false, "only write the unit file; print the commands to enable it yourself instead of running them")
	return cmd
}
