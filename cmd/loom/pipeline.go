package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mcuste/loom/pkg/runner"
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
		// Use the partial bag carried by the error so MISSING entries still
		// surface in the printed plan rather than the section truncating silently.
		// The merge order is authoritative in ResolveParams; no local rebuild needed.
		resolved = miss.Partial
	}
	if err := r.Plan(wf, resolved, cliParams, seeded); err != nil {
		return nil, err
	}
	return resolved, nil
}

// loadWorkflow resolves a workflow ref to a file, reads and inlines its
// prompt_file references, parses it, links and statically validates its
// sub-workflows, and runs the routing check. It is the shared prelude behind
// `loom run` and the scheduler: a workflow that fails to load is rejected when
// the command is issued, not at fire time. It returns the parsed workflow, the
// inlined manifest bytes (what the store persists), and the resolved absolute
// path the schedule records so the daemon can reload it from its own cwd.
func loadWorkflow(home, ref string) (*workflow.Workflow, []byte, string, error) {
	path, err := resolveWorkflowRef(home, ref)
	if err != nil {
		return nil, nil, "", err
	}
	// Make the path absolute so the scheduler can reload the file regardless of
	// its own working directory, and so callers do not need a separate abs step.
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("resolve workflow path %s: %w", path, err)
	}
	wf, manifest, err := workflow.ReadAndParse(path)
	if err != nil {
		return nil, nil, "", err
	}
	// Resolve and link any `workflow:` children from disk, statically validate
	// them, and run the routing check, so a bad sub-workflow ref or route fails
	// before any model call.
	if err := workflow.Link(wf, path, func(ref, parentDir string) (string, error) {
		return resolveSubWorkflowRef(home, ref, parentDir)
	}); err != nil {
		return nil, nil, "", err
	}
	return wf, manifest, path, nil
}

// renderCheckRun runs the shared check phase (validate + print the plan) against
// a single renderer and, only if it passes, executes req. doRun and runFromRecord
// share this tail: one renderer drives both the check and the run that follows, so
// a stateful display spans both. params carries the CLI and (lower-precedence)
// file tiers; seeded annotates the plan with carried-over tasks. The caller
// fills req.Wf, req.Manifest, req.Home, req.Cwd, and any seed plan; the
// resolved params come from the check done here.
func renderCheckRun(w io.Writer, req runner.Request, params paramInputs, seeded map[workflow.TaskID]bool) (err error) {
	// Resolve cwd here when the caller did not supply one, so Run's store open
	// records the directory this invocation is launched from. Callers that
	// chdir before reaching here (resume paths) set req.Cwd explicitly so the
	// record stores the restored directory, not the mid-chdir one.
	if req.Cwd == "" {
		cwd, cwderr := os.Getwd()
		if cwderr != nil {
			return fmt.Errorf("resolve working directory: %w", cwderr)
		}
		req.Cwd = cwd
	}
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
