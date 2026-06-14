// Command loom validates and runs workflow YAML files.
//
// Usage:
//
//	loom run <workflow.yaml> [-p key=val ...]    validate, print execution order, and run
//	loom check <workflow.yaml> [-p key=val ...]  validate and print execution order only
//	loom --help                                  show usage
//
// The plan, per-task progress, and the final summary are written to stdout.
// Exit code 0 on success, 1 on any validation or execution error.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/store"
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
	root.AddCommand(newRunCmd(), newCheckCmd())
	return root
}

func newRunCmd() *cobra.Command {
	var paramArgs []string
	cmd := &cobra.Command{
		Use:   "run <workflow.yaml>",
		Short: "Validate and run a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doRun(cmd.OutOrStdout(), args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	return cmd
}

func newCheckCmd() *cobra.Command {
	var paramArgs []string
	cmd := &cobra.Command{
		Use:   "check <workflow.yaml>",
		Short: "Validate a workflow and print execution order",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doCheck(cmd.OutOrStdout(), args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	return cmd
}

// addParamFlags registers the repeatable `-p` / `--param` flag for passing
// workflow params on the command line. StringArrayVarP is used (not
// StringSliceVarP) so commas inside values are preserved verbatim.
func addParamFlags(cmd *cobra.Command, params *[]string) {
	cmd.Flags().StringArrayVarP(params, "param", "p", nil,
		"set a workflow parameter (repeatable), e.g. -p env=prod")
}

func doRun(w io.Writer, path string, paramArgs []string) error {
	manifest, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return err
	}
	cliParams, err := workflow.ParseParamArgs(paramArgs)
	if err != nil {
		return err
	}
	resolved, err := workflow.ResolveParams(wf, cliParams, nil)
	if err != nil {
		return err
	}
	printPlan(w, wf, resolved, cliParams)
	fmt.Fprintln(w)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	run, err := store.Open(wf.ID, manifest, store.Config{
		OnError: func(e error) { fmt.Fprintf(w, "  store: %v\n", e) },
		Params:  stringifyParams(resolved),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Run file : %s\n\n", run.Path())

	rep, err := executor.Run(ctx, wf, executor.JoinHooks(
		hooks(w, len(wf.Tasks)),
		storeHooks(run),
	), executor.Options{Params: resolved})
	if closeErr := run.Close(summaryFor(rep), err); closeErr != nil {
		fmt.Fprintf(w, "  store: %v\n", closeErr)
	}
	if rep != nil {
		printSummary(w, wf, rep)
	}
	return err
}

// doCheck mirrors doRun's parse + ResolveParams pipeline, then exits without
// invoking the executor. A MissingRequiredParamError is treated as advisory
// (warning + exit 0) so `loom check` doubles as a "what params does this
// workflow need?" probe; CLI-hygiene errors (unknown keys, malformed args,
// duplicates) remain hard failures.
func doCheck(w io.Writer, path string, paramArgs []string) error {
	wf, err := workflow.ParseFile(path)
	if err != nil {
		return err
	}
	cliParams, err := workflow.ParseParamArgs(paramArgs)
	if err != nil {
		return err
	}
	resolved, err := workflow.ResolveParams(wf, cliParams, nil)
	if err != nil {
		var miss *workflow.MissingRequiredParamError
		if errors.As(err, &miss) {
			fmt.Fprintf(w, "warning: required param %q not supplied\n", miss.Name)
			// Rebuild a partial bag so MISSING entries still surface in the
			// printed plan rather than the section truncating silently.
			resolved = partialResolved(wf, cliParams)
		} else {
			return err
		}
	}
	printPlan(w, wf, resolved, cliParams)
	return nil
}

// partialResolved rebuilds the resolved bag without the missing-required
// check, so `loom check` can still print a meaningful plan when a required
// param is absent. Keeps the merge order identical to ResolveParams.
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

// stringifyParams converts a ParamValues bag into the plain string map the
// store package persists. nil in → nil out so an empty params block stays
// absent from the JSON via `omitempty`.
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

// storeHooks adapts a store.Run into executor.Hooks: OnStart passes through
// unchanged; OnFinish translates the executor's TaskResult into the store's
// own type at the boundary so the store package stays independent of the
// executor.
func storeHooks(run *store.Run) executor.Hooks {
	finish := run.OnFinish()
	return executor.Hooks{
		OnStart: run.OnStart(),
		OnFinish: func(t workflow.Task, res executor.TaskResult, err error) {
			finish(t, store.TaskResult{
				Prompt:  res.Prompt,
				Output:  res.Output,
				Usage:   res.Usage,
				Elapsed: res.Elapsed,
			}, err)
		},
	}
}

// summaryFor projects an executor.Report into the slim store.Summary the
// store needs at Close time. Returns nil when rep is nil so Close leaves the
// totals unset.
func summaryFor(rep *executor.Report) *store.Summary {
	if rep == nil {
		return nil
	}
	return &store.Summary{Usage: rep.Usage, TaskCount: len(rep.Tasks)}
}

// printPlan writes the workflow header, the params block (when present), and
// the task execution order to w. It uses workflow.Effective so the printed
// runtime/model/effort match what the runtime will actually see. The cli arg
// drives the per-param provenance tag (cli vs default vs MISSING); the
// resolved bag drives the printed value.
func printPlan(w io.Writer, wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string) {
	fmt.Fprintf(w, "Workflow : %s\n", wf.ID)
	if wf.Description != "" {
		fmt.Fprintf(w, "Desc     : %s\n", wf.Description)
	}
	fmt.Fprintf(w, "Runtime  : %s\n", orDash(string(wf.Runtime)))
	fmt.Fprintf(w, "Model    : %s\n", orDash(string(wf.Model)))
	fmt.Fprintf(w, "Effort   : %s\n", orDash(string(wf.Effort)))
	if wf.SystemPrompt != "" {
		fmt.Fprintf(w, "System   : %s\n", wf.SystemPrompt)
	}

	if len(wf.Params) > 0 {
		nameWidth := 0
		for _, p := range wf.Params {
			if n := len(p.Name); n > nameWidth {
				nameWidth = n
			}
		}
		fmt.Fprintf(w, "\nParams (%d):\n", len(wf.Params))
		for _, p := range wf.Params {
			value, ok := resolved[p.Name]
			source := paramSource(p, cli, ok)
			if !ok {
				fmt.Fprintf(w, "  %-*s = %-12s (%s)\n", nameWidth, p.Name, "<missing>", source)
				continue
			}
			fmt.Fprintf(w, "  %-*s = %-12s (%s)\n", nameWidth, p.Name, quoteIfNeeded(value), source)
		}
	}

	order := wf.Plan()
	fmt.Fprintf(w, "\nExecution order (%d task%s):\n", len(order), plural(len(order)))

	idWidth := 0
	for _, id := range order {
		if n := len(id); n > idWidth {
			idWidth = n
		}
	}

	for i, id := range order {
		t := wf.ByID(id)
		if t.IsShell() {
			cmd := t.Command
			if len(cmd) > 60 {
				cmd = cmd[:60] + "…"
			}
			fmt.Fprintf(w, "  %2d. %-*s  kind=shell  cmd=%q  deps=%s\n",
				i+1, idWidth, id, cmd, depsList(t.DependsOn))
		} else {
			rt, m, e := wf.Effective(t)
			fmt.Fprintf(w, "  %2d. %-*s  runtime=%-12s  model=%-8s  effort=%-7s  deps=%s\n",
				i+1, idWidth, id, orDash(string(rt)), orDash(string(m)), orDash(string(e)), depsList(t.DependsOn))
		}
	}
}

// paramSource picks the provenance tag for a declared param given the CLI map
// and whether the resolved bag carried a value. `cli` wins over `default`;
// absence is reported as `MISSING`. (No --params-file yet — v2 follow-up.)
func paramSource(p workflow.Param, cli map[string]string, resolvedHasValue bool) string {
	if _, ok := cli[string(p.Name)]; ok {
		return "cli"
	}
	if resolvedHasValue {
		return "default"
	}
	return "MISSING"
}

// quoteIfNeeded surrounds a value with quotes only when it would otherwise
// be ambiguous in the printed plan (empty string, or leading/trailing
// whitespace). Keeps the common case readable while preserving fidelity for
// the corner cases.
func quoteIfNeeded(s string) string {
	if s == "" || strings.TrimSpace(s) != s {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// hooks returns executor.Hooks that write per-task progress lines to w.
// Under concurrent execution, OnStart and OnFinish for different tasks
// interleave; the mutex serializes writes so a line never tears, and the
// finish line carries the task id so output is still readable.
func hooks(w io.Writer, total int) executor.Hooks {
	var (
		step atomic.Int32
		mu   sync.Mutex
	)
	return executor.Hooks{
		OnStart: func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			n := step.Add(1)
			mu.Lock()
			if rt == "" {
				fmt.Fprintf(w, "[%d/%d] %s (shell)\n", n, total, t.ID)
			} else {
				fmt.Fprintf(w, "[%d/%d] %s (%s/%s%s)\n", n, total, t.ID, rt, m, effortSuffix(e))
			}
			mu.Unlock()
		},
		OnFinish: func(t workflow.Task, res executor.TaskResult, err error) {
			mu.Lock()
			defer mu.Unlock()
			if t.IsShell() {
				if err != nil {
					fmt.Fprintf(w, "  %s FAIL after %s: %v\n", t.ID, res.Elapsed.Round(time.Millisecond), err)
					return
				}
				fmt.Fprintf(w, "  %s done %s  exit=0\n", t.ID, res.Elapsed.Round(time.Millisecond))
				return
			}
			if err != nil {
				fmt.Fprintf(w, "  %s FAIL after %s: %v\n", t.ID, res.Elapsed.Round(time.Millisecond), err)
				return
			}
			fmt.Fprintf(w, "  %s done %s  in=%d out=%d cache=%d  $%.6f\n",
				t.ID, res.Elapsed.Round(time.Millisecond),
				res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CacheReadTokens, res.Usage.TotalCostUSD)
		},
	}
}

// printSummary writes the final cost and token totals after a run.
func printSummary(w io.Writer, wf *workflow.Workflow, rep *executor.Report) {
	const bar = "────────────────────────────────────────"
	fmt.Fprintf(w, "\n%s\n", bar)
	fmt.Fprintf(w, "  total tokens : %d in / %d out / %d cache-read\n",
		rep.Usage.InputTokens, rep.Usage.OutputTokens, rep.Usage.CacheReadTokens)
	fmt.Fprintf(w, "  total cost   : $%.6f\n", rep.Usage.TotalCostUSD)
	fmt.Fprintf(w, "%s\n", bar)
	if len(rep.Tasks) == len(wf.Tasks) {
		fmt.Fprintf(w, "✓ workflow %q complete\n", wf.ID)
	} else {
		fmt.Fprintf(w, "✗ workflow %q stopped after %d/%d tasks\n", wf.ID, len(rep.Tasks), len(wf.Tasks))
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func effortSuffix(e runtime.Effort) string {
	if e == "" {
		return ""
	}
	return "/" + string(e)
}

func depsList(deps []workflow.TaskID) string {
	if len(deps) == 0 {
		return "none"
	}
	parts := make([]string, len(deps))
	for i, d := range deps {
		parts[i] = string(d)
	}
	return strings.Join(parts, ",")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
