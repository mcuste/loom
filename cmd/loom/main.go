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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"

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
	root.AddCommand(newRunCmd(), newResumeCmd(), newRunsCmd(), newWorkflowsCmd())
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

// check is the validation phase shared by `loom check` and `loom run`: it
// parses the CLI params, resolves them against wf, and prints the execution
// plan. Routing `run` through it is what makes "run does a check first" true --
// both commands reach the same validate-and-print routine before anything
// executes. file is the lower-precedence param tier (a record's stored params
// on resume; nil otherwise). advisory downgrades a missing required param to a
// warning so `loom check` doubles as a "what params does this workflow need?"
// probe; `loom run` passes false so the same case is a hard failure. seeded
// annotates the plan with carried-over tasks on resume. It returns the resolved
// params (and CLI param map) for the caller to execute with.
func check(r tui.Renderer, wf *workflow.Workflow, paramArgs []string, file map[string]string, advisory bool, seeded map[workflow.TaskID]bool) (workflow.ParamValues, map[string]string, error) {
	cliParams, err := workflow.ParseParamArgs(paramArgs)
	if err != nil {
		return nil, nil, err
	}
	resolved, err := workflow.ResolveParams(wf, cliParams, file)
	if err != nil {
		var miss *workflow.MissingRequiredParamError
		if !advisory || !errors.As(err, &miss) {
			return nil, nil, err
		}
		// Route the advisory through the renderer so every stdout byte flows
		// through the seam, not around it.
		if werr := r.Warn(fmt.Sprintf("required param %q not supplied", miss.Name)); werr != nil {
			return nil, nil, werr
		}
		// Rebuild a partial bag so MISSING entries still surface in the printed
		// plan rather than the section truncating silently.
		resolved = partialResolved(wf, cliParams)
	}
	if err := r.Plan(wf, resolved, cliParams, seeded); err != nil {
		return nil, nil, err
	}
	return resolved, cliParams, nil
}

// doRun runs the shared check phase (validate + print the plan) and then, only
// if it passes, executes the whole workflow fresh.
func doRun(w io.Writer, path string, paramArgs []string) (err error) {
	path, err = resolveWorkflowRef(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Inline `prompt_file:` references relative to the workflow's directory, then
	// parse the inlined bytes. The inlined manifest is what gets stored, so the
	// run record stays self-contained even if the referenced files later change.
	manifest, err := workflow.InlinePromptFiles(raw, filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return err
	}
	// Resolve and link any `workflow:` children from disk, then statically
	// validate them, so a bad sub-workflow ref fails before any model call.
	if err := linkSubWorkflows(wf, path, nil); err != nil {
		return err
	}
	if err := checkSubWorkflows(wf); err != nil {
		return err
	}
	// One renderer drives both phases (check's plan and runWorkflow's header,
	// progress, and summary) so a stateful renderer can hold a unified display
	// across them. Its teardown error surfaces unless a prior error already won.
	r := tui.New(w)
	defer func() {
		if cerr := r.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	resolved, _, err := check(r, wf, paramArgs, nil, false, nil)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	home, err := loomHome()
	if err != nil {
		return err
	}
	return runWorkflow(r, w, home, manifest, wf, resolved, seedPlan{})
}

// doCheck runs the shared check phase only: validate and print the plan, then
// stop without executing.
func doCheck(w io.Writer, path string, paramArgs []string) (err error) {
	path, err = resolveWorkflowRef(path)
	if err != nil {
		return err
	}
	wf, err := workflow.ParseFile(path)
	if err != nil {
		return err
	}
	if err := linkSubWorkflows(wf, path, nil); err != nil {
		return err
	}
	if err := checkSubWorkflows(wf); err != nil {
		return err
	}
	r := tui.New(w)
	defer func() {
		if cerr := r.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	_, _, err = check(r, wf, paramArgs, nil, true, nil)
	return err
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

// stringifyParams returns nil for an empty bag so `omitempty` keeps params
// absent from the stored JSON rather than writing an empty object.
func stringifyParams(p workflow.ParamValues) map[string]string {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[string(k)] = v
	}
	return out
}

// storeHooks binds store.Run.OnStart and store.Run.OnFinish as method values
// directly: their signatures match executor.Hooks with no adapter needed.
func storeHooks(run *store.Run) executor.Hooks {
	return executor.Hooks{
		OnStart:  run.OnStart,
		OnFinish: run.OnFinish,
	}
}

// summaryFor returns nil when rep is nil so store.Run.Close leaves totals unset.
func summaryFor(rep *executor.Report) *store.Summary {
	if rep == nil {
		return nil
	}
	return &store.Summary{Usage: rep.Usage, TaskCount: len(rep.Tasks)}
}
