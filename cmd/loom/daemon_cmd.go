package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/internal/daemoninstall"
	"github.com/mcuste/loom/pkg/interpreter"
	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/scheduler"
	"github.com/mcuste/loom/pkg/tui"
)

func newDaemonCmd(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the scheduler loop that fires scheduled workflows",
		Long: "Run the scheduler loop in the foreground. It reads schedules from " +
			"$LOOM_HOME/schedules and fires each workflow at its cron time or one-off " +
			"instant, recording every run in the normal run store. Use a process " +
			"supervisor (launchd/systemd) to keep it alive across reboots.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := interruptContext()
			defer stop()
			launcher := interpreter.FileRunLauncher{
				Home:    env.home,
				Cwd:     env.cwd,
				Catalog: env.catalog,
				NewObserver: func(w io.Writer) runner.Observer {
					return tui.New(w)
				},
				LogRoot: filepath.Join(schedule.SchedulesDir(env.home), "logs"),
			}
			d := scheduler.New(env.home, env.cwd, launcher, cmd.OutOrStdout())
			return d.Run(ctx)
		},
	}
	cmd.AddCommand(newDaemonInstallCmd(env))
	return cmd
}

// newDaemonInstallCmd writes a platform supervisor unit (launchd on macOS,
// systemd on Linux) that keeps `loom daemon` running across logout and reboot,
// and by default loads it so the daemon starts immediately. Pass --manual to
// only write the unit and print the commands to enable it yourself.
func newDaemonInstallCmd(env *cliEnv) *cobra.Command {
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
			return daemoninstall.Install(cmd.OutOrStdout(), exec, env.home, manual)
		},
	}
	cmd.Flags().BoolVar(&manual, "manual", false, "only write the unit file; print the commands to enable it yourself instead of running them")
	return cmd
}
