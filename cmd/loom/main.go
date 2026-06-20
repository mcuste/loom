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
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	root.AddCommand(newRunCmd(), newCheckCmd(), newResumeCmd())
	return root
}

func newRunCmd() *cobra.Command {
	var (
		paramArgs    []string
		resumeLatest bool
	)
	cmd := &cobra.Command{
		Use:   "run <workflow.yaml>",
		Short: "Validate and run a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if resumeLatest {
				return doRunResumeLatest(cmd.OutOrStdout(), args[0], paramArgs)
			}
			return doRun(cmd.OutOrStdout(), args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	cmd.Flags().BoolVar(&resumeLatest, "resume-latest", false,
		"seed ok tasks from .loom/runs/<wf>/latest.json and re-run the remainder")
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

// addParamFlags uses StringArrayVarP (not StringSliceVarP) so commas inside
// values are preserved verbatim.
func addParamFlags(cmd *cobra.Command, params *[]string) {
	cmd.Flags().StringArrayVarP(params, "param", "p", nil,
		"set a workflow parameter (repeatable), e.g. -p env=prod")
}

// doRun passes an empty seed to runWorkflow so every task executes fresh.
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
	return runWorkflow(w, manifest, wf, resolved, cliParams, seedPlan{})
}

// doCheck treats MissingRequiredParamError as advisory (warning + exit 0) so
// `loom check` doubles as a "what params does this workflow need?" probe;
// CLI-hygiene errors (unknown keys, malformed args, duplicates) are still
// hard failures.
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
			if _, werr := fmt.Fprintf(w, "warning: required param %q not supplied\n", miss.Name); werr != nil {
				return werr
			}
			// Rebuild a partial bag so MISSING entries still surface in the
			// printed plan rather than the section truncating silently.
			resolved = partialResolved(wf, cliParams)
		} else {
			return err
		}
	}
	printPlan(w, wf, resolved, cliParams, nil)
	return nil
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
// directly — their signatures match executor.Hooks with no adapter needed.
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

// printPlan uses workflow.Effective so printed runtime/model/effort match what
// the runtime will actually see. cli drives the per-param provenance tag
// (cli vs default vs MISSING). When seeded is non-empty the section header
// separates the seeded count so a resume plan shows which steps are skipped.
func printPlan(w io.Writer, wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string, seeded map[workflow.TaskID]bool) {
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
	seedCount := 0
	for _, id := range order {
		if seeded[id] {
			seedCount++
		}
	}
	if seedCount > 0 {
		fmt.Fprintf(w, "\nExecution order (%d task%s; %d seeded):\n", len(order), plural(len(order)), seedCount)
	} else {
		fmt.Fprintf(w, "\nExecution order (%d task%s):\n", len(order), plural(len(order)))
	}

	idWidth := 0
	for _, id := range order {
		if n := len(id); n > idWidth {
			idWidth = n
		}
	}

	for i, id := range order {
		t := wf.ByID(id)
		suffix := ""
		if seeded[id] {
			suffix = "  (seeded; using stored output)"
		}
		if t.IsShell() {
			cmd := t.Command
			if len(cmd) > 60 {
				cmd = cmd[:60] + "…"
			}
			fmt.Fprintf(w, "  %2d. %-*s  kind=shell  cmd=%q  deps=%s%s\n",
				i+1, idWidth, id, cmd, depsList(t.DependsOn), suffix)
		} else {
			rt, m, e := wf.Effective(t)
			fmt.Fprintf(w, "  %2d. %-*s  runtime=%-12s  model=%-8s  effort=%-7s  deps=%s%s\n",
				i+1, idWidth, id, orDash(string(rt)), orDash(string(m)), orDash(string(e)), depsList(t.DependsOn), suffix)
		}
	}
}

func paramSource(p workflow.Param, cli map[string]string, resolvedHasValue bool) string {
	if _, ok := cli[string(p.Name)]; ok {
		return "cli"
	}
	if resolvedHasValue {
		return "default"
	}
	return "MISSING"
}

func quoteIfNeeded(s string) string {
	if s == "" || strings.TrimSpace(s) != s {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// hooks serializes concurrent OnStart/OnFinish writes behind a mutex so
// output lines never interleave mid-write.
func hooks(w io.Writer, total int) executor.Hooks {
	var (
		step atomic.Int32
		mu   sync.Mutex
	)
	return executor.Hooks{
		OnStart: func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			n := step.Add(1)
			mu.Lock()
			defer mu.Unlock()
			if t.IsShell() {
				fmt.Fprintf(w, "[%d/%d] %s (shell)\n", n, total, t.ID)
			} else {
				fmt.Fprintf(w, "[%d/%d] %s (%s/%s%s)\n", n, total, t.ID, rt, m, effortSuffix(e))
			}
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

// printSummary compares len(rep.Tasks) against expected to choose the success
// or partial-failure line. expected is the full task count for `loom run` and
// the non-seeded count for `loom resume`.
func printSummary(w io.Writer, wf *workflow.Workflow, rep *executor.Report, expected int) {
	const bar = "────────────────────────────────────────"
	fmt.Fprintf(w, "\n%s\n", bar)
	fmt.Fprintf(w, "  total tokens : %d in / %d out / %d cache-read\n",
		rep.Usage.InputTokens, rep.Usage.OutputTokens, rep.Usage.CacheReadTokens)
	fmt.Fprintf(w, "  total cost   : $%.6f\n", rep.Usage.TotalCostUSD)
	fmt.Fprintf(w, "%s\n", bar)
	if len(rep.Tasks) == expected {
		fmt.Fprintf(w, "✓ workflow %q complete\n", wf.ID)
	} else {
		fmt.Fprintf(w, "✗ workflow %q stopped after %d/%d tasks\n", wf.ID, len(rep.Tasks), expected)
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
