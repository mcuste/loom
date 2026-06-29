package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

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

// newRenderer creates a renderer over w and returns it alongside a closer to
// defer. The closer surfaces the renderer's teardown error through errp unless a
// prior error already won, so a caller with a named return writes:
//
//	r, finish := newRenderer(w)
//	defer finish(&err)
//
// One renderer drives a command's check phase and the run that follows, so a
// stateful renderer can hold a unified display across both.
func newRenderer(w io.Writer) (tui.Renderer, func(*error)) {
	r := tui.New(w)
	return r, func(errp *error) {
		if cerr := r.Close(); cerr != nil && *errp == nil {
			*errp = cerr
		}
	}
}

// paramInputs carries the two param tiers that always travel together into the
// resolver: cli is the repeatable -p args (highest precedence) and file is the
// lower-precedence tier (a run record's stored params on resume; nil for a
// fresh run or `loom check`).
type paramInputs struct {
	cli  []string
	file map[string]string
}

// validateAndPlan is the validation phase shared by `loom check` and `loom run`:
// it parses the CLI params, resolves them against wf, and prints the execution
// plan. Routing `run` through it is what makes "run does a check first" true --
// both commands reach the same validate-and-print routine before anything
// executes. params carries the CLI and (lower-precedence) file tiers. advisory
// downgrades a missing required param to a warning so `loom check` doubles as a
// "what params does this workflow need?" probe; `loom run` passes false so the
// same case is a hard failure. seeded annotates the plan with carried-over
// tasks on resume. It returns the resolved params for the caller to execute
// with.
func validateAndPlan(r tui.Renderer, wf *workflow.Workflow, params paramInputs, advisory bool, seeded map[workflow.TaskID]bool) (workflow.ParamValues, error) {
	cliParams, err := workflow.ParseParamArgs(params.cli)
	if err != nil {
		return nil, err
	}
	resolved, err := workflow.ResolveParams(wf, cliParams, params.file)
	if err != nil {
		var miss *workflow.MissingRequiredParamError
		if !advisory || !errors.As(err, &miss) {
			return nil, err
		}
		// Route the advisory through the renderer so every stdout byte flows
		// through the seam, not around it.
		if werr := r.Warn(fmt.Sprintf("required param %q not supplied", miss.Name)); werr != nil {
			return nil, werr
		}
		// Rebuild a partial bag so MISSING entries still surface in the printed
		// plan rather than the section truncating silently.
		resolved = partialResolved(wf, cliParams)
	}
	if err := r.Plan(wf, resolved, cliParams, seeded); err != nil {
		return nil, err
	}
	return resolved, nil
}

// partialResolved keeps the merge order identical to ResolveParams.
func partialResolved(wf *workflow.Workflow, cli map[string]string) workflow.ParamValues {
	out := make(workflow.ParamValues, len(wf.Params))
	for _, p := range wf.Params {
		if p.HasDefault {
			out[p.Name] = p.Default
		}
	}
	for k, v := range cli {
		out[workflow.ParamName(k)] = v
	}
	return out
}

// doCheck runs the shared validation phase only: validate and print the plan,
// then stop without executing.
func doCheck(w io.Writer, path string, paramArgs []string) (err error) {
	path, err = resolveWorkflowRef(path)
	if err != nil {
		return err
	}
	wf, err := workflow.ParseFile(path)
	if err != nil {
		return err
	}
	// ParseFile validated the top-level routing; linkAndValidate re-runs it after
	// linking so any sub-workflow children are checked too.
	if err := linkAndValidate(wf, path); err != nil {
		return err
	}
	r, finish := newRenderer(w)
	defer finish(&err)
	_, err = validateAndPlan(r, wf, paramInputs{cli: paramArgs}, true, nil)
	return err
}
