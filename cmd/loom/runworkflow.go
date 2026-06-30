package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/workflow"
)

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

// doRun runs the shared check phase (validate + print the plan) and then, only
// if it passes, executes the whole workflow fresh. home is resolved up front (as
// the resume paths do) so a home-resolution failure surfaces before the plan.
func doRun(w io.Writer, path string, paramArgs []string) error {
	wf, manifest, _, err := loadWorkflow(path)
	if err != nil {
		return err
	}
	home, err := loomHome()
	if err != nil {
		return err
	}
	return renderCheckRun(w, runner.Request{Wf: wf, Manifest: manifest, Home: home}, paramInputs{cli: paramArgs}, nil)
}

// renderCheckRun runs the shared check phase (validate + print the plan) against
// a single renderer and, only if it passes, executes req. doRun and runFromRecord
// share this tail: one renderer drives both the check and the run that follows, so
// a stateful display spans both. params carries the CLI and (lower-precedence)
// file tiers; seeded annotates the plan with carried-over tasks. The caller
// fills req.Wf, req.Manifest, req.Home, and any seed plan; the resolved params
// come from the check done here.
func renderCheckRun(w io.Writer, req runner.Request, params paramInputs, seeded map[workflow.TaskID]bool) (err error) {
	r, finish := newRenderer(w)
	defer finish(&err)
	resolved, err := validateAndPlan(r, req.Wf, params, false, seeded)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	req.Resolved = resolved
	ctx, stop := interruptContext()
	defer stop()
	_, err = runner.Run(ctx, r, w, req)
	return err
}
