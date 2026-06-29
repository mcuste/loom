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
	"os"

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
	root := &cobra.Command{
		Use:          "loom",
		Short:        "Validate and run workflow YAML files",
		SilenceUsage: true,
	}
	root.AddCommand(newRunCmd(), newResumeCmd(), newRunsCmd(), newWorkflowsCmd(), newScheduleCmd(), newDaemonCmd())
	return root
}

// newRunCmd is the parent for executing a workflow. Invoked with a workflow
// path it validates and runs (or resumes the latest run with --resume-latest);
// its `check` subcommand stops after validation and the printed plan. A path
// that is not a subcommand routes to the parent (cobra runs it when args[0]
// does not name a child), so `loom run wf.yaml` executes as before.
func newRunCmd() *cobra.Command {
	var (
		paramArgs    []string
		resumeLatest bool
	)
	cmd := &cobra.Command{
		Use:               "run <workflow>",
		Short:             "Validate and run a workflow",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkflowRef,
		RunE: func(cmd *cobra.Command, args []string) error {
			if resumeLatest {
				return doRunResumeLatest(cmd.OutOrStdout(), args[0], paramArgs)
			}
			return doRun(cmd.OutOrStdout(), args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	cmd.Flags().BoolVar(&resumeLatest, "resume-latest", false,
		"seed ok tasks from $LOOM_HOME/runs/<wf>/latest.json (default $HOME/.loom) and re-run the remainder")
	cmd.AddCommand(newCheckCmd())
	return cmd
}

func newCheckCmd() *cobra.Command {
	var paramArgs []string
	cmd := &cobra.Command{
		Use:   "check <workflow>",
		Short: "Validate a workflow and print its execution plan, without running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doCheck(cmd.OutOrStdout(), args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	return cmd
}

// addParamFlags uses StringArrayVarP (not StringSliceVarP) so commas inside
// values are preserved verbatim.
func addParamFlags(cmd *cobra.Command, params *[]string) {
	cmd.Flags().StringArrayVarP(params, "param", "p", nil,
		"set a workflow parameter (repeatable), e.g. -p env=prod")
}

// firstArg returns the optional single positional argument (a workflow filter
// for `runs`/`schedule ls`/`schedule sync`), or "" when absent.
func firstArg(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return ""
}
