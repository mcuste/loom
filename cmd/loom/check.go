package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

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

// validateAndPlan is the validation phase shared by `loom check` and `loom run`:
// it parses the CLI params, resolves them against wf, and prints the execution
// plan. Routing `run` through it is what makes "run does a check first" true --
// both commands reach the same validate-and-print routine before anything
// executes. file is the lower-precedence param tier (a record's stored params
// on resume; nil otherwise). advisory downgrades a missing required param to a
// warning so `loom check` doubles as a "what params does this workflow need?"
// probe; `loom run` passes false so the same case is a hard failure. seeded
// annotates the plan with carried-over tasks on resume. It returns the resolved
// params for the caller to execute with.
func validateAndPlan(r tui.Renderer, wf *workflow.Workflow, paramArgs []string, file map[string]string, advisory bool, seeded map[workflow.TaskID]bool) (workflow.ParamValues, error) {
	cliParams, err := workflow.ParseParamArgs(paramArgs)
	if err != nil {
		return nil, err
	}
	resolved, err := workflow.ResolveParams(wf, cliParams, file)
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
	_, err = validateAndPlan(r, wf, paramArgs, nil, true, nil)
	return err
}
