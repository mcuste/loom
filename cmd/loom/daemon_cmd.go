package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
			d := newDaemon(home, cmd.OutOrStdout())
			return d.run(ctx)
		},
	}
	cmd.AddCommand(newDaemonInstallCmd())
	return cmd
}

// newDaemonInstallCmd writes a platform supervisor unit (launchd on macOS,
// systemd on Linux) that keeps `loom daemon` running across logout and reboot.
func newDaemonInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install a launchd/systemd unit that keeps `loom daemon` running",
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
			return installDaemon(cmd.OutOrStdout(), exec, home)
		},
	}
}
