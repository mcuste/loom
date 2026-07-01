package main

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflowload"
)

// newRunCmd is the parent for executing a workflow. Invoked with a workflow
// path it validates and runs (or resumes the latest run with --resume-latest);
// its `check` subcommand stops after validation and the printed plan. A path
// that is not a subcommand routes to the parent (cobra runs it when args[0]
// does not name a child), so `loom run wf.yaml` executes as before.
func newRunCmd(env *cliEnv) *cobra.Command {
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
				return doRunResumeLatest(cmd.OutOrStdout(), env.home, env.cwd, env.catalog, args[0], paramArgs)
			}
			return doRun(cmd.OutOrStdout(), env.home, env.cwd, env.catalog, args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	cmd.Flags().BoolVar(&resumeLatest, "resume-latest", false,
		"seed ok tasks from $LOOM_HOME/runs/<wf>/latest.json (default $HOME/.loom) and re-run the remainder")
	cmd.AddCommand(newCheckCmd(env))
	return cmd
}

// doRun runs the shared check phase (validate + print the plan) and then, only
// if it passes, executes the whole workflow fresh. home is resolved up front (as
// the resume paths do) so a home-resolution failure surfaces before the plan.
func doRun(w io.Writer, home, cwd string, catalog runtime.Catalog, path string, paramArgs []string) error {
	wf, manifest, _, err := workflowload.Load(home, cwd, path)
	if err != nil {
		return err
	}
	return renderCheckRun(w, runner.Request{Wf: wf, Manifest: manifest, Catalog: catalog, Home: home}, paramInputs{cli: paramArgs}, nil)
}
