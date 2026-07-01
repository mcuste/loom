// Command loom validates and runs workflow YAML files.
//
// Usage:
//
//	loom run <workflow> [-p key=val ...]    validate, print execution order, and run
//	loom check <workflow> [-p key=val ...]  validate and print execution order only
//	loom --help                             show usage
//
// <workflow> is a YAML path or a registry name resolved under $LOOM_HOME/workflows
// (':' is the hierarchy separator); `loom workflows ls` lists the registry.
//
// The plan, per-task progress, and the final summary are written to stdout.
// Exit code 0 on success, 1 on any validation or execution error.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	// Side-effect imports register runtimes with the runtime package.
	_ "github.com/mcuste/loom/pkg/runtime/claudecode"
	_ "github.com/mcuste/loom/pkg/runtime/codex"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	env := &cliEnv{}
	root := &cobra.Command{
		Use:          "loom",
		Short:        "Validate and run workflow YAML files",
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			h, err := loomHome()
			if err != nil {
				return err
			}
			env.home = h
			return nil
		},
	}
	root.AddCommand(newRunCmd(env), newResumeCmd(env), newRunsCmd(env), newWorkflowsCmd(env), newScheduleCmd(env), newDaemonCmd(env))
	return root
}

// interruptContext returns a context cancelled on the first SIGINT or SIGTERM,
// the shared graceful-shutdown signal for the run pipeline (runWorkflow) and the
// scheduler daemon. The returned stop must be deferred to release the handler.
func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
