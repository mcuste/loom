// Command loom validates and runs workflow YAML files.
//
// Usage:
//
//	loom run <workflow.yaml>      validate, print execution order, and run
//	loom check <workflow.yaml>    validate and print execution order only
//	loom --help                   show usage
//
// The plan, per-task progress, and the final summary are written to stdout.
// Exit code 0 on success, 1 on any validation or execution error.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"

	// Side-effect imports register runtimes with the runtime package.
	_ "github.com/mcuste/loom/pkg/runtime/claudecode"
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
	return &cobra.Command{
		Use:   "run <workflow.yaml>",
		Short: "Validate and run a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doRun(cmd.OutOrStdout(), args[0])
		},
	}
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <workflow.yaml>",
		Short: "Validate a workflow and print execution order",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wf, err := workflow.ParseFile(args[0])
			if err != nil {
				return err
			}
			printPlan(cmd.OutOrStdout(), wf)
			return nil
		},
	}
}

func doRun(w io.Writer, path string) error {
	wf, err := workflow.ParseFile(path)
	if err != nil {
		return err
	}
	printPlan(w, wf)
	fmt.Fprintln(w)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rep, err := executor.Run(ctx, wf, hooks(w, len(wf.Tasks)))
	if rep != nil {
		printSummary(w, wf, rep)
	}
	return err
}

// printPlan writes the workflow header and the task execution order to w. It
// uses workflow.Effective so the printed runtime/model/effort match what the
// runtime will actually see.
func printPlan(w io.Writer, wf *workflow.Workflow) {
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
		rt, m, e := wf.Effective(*t)
		fmt.Fprintf(w, "  %2d. %-*s  runtime=%-12s  model=%-8s  effort=%-7s  deps=%s\n",
			i+1, idWidth, id, orDash(string(rt)), orDash(string(m)), orDash(string(e)), depsList(t.DependsOn))
	}
}

// hooks returns executor.Hooks that write per-task progress lines to w.
func hooks(w io.Writer, total int) executor.Hooks {
	step := 0
	return executor.Hooks{
		OnStart: func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			step++
			fmt.Fprintf(w, "[%d/%d] %s (%s/%s%s)\n", step, total, t.ID, rt, m, effortSuffix(e))
		},
		OnFinish: func(t workflow.Task, res executor.TaskResult, err error) {
			if err != nil {
				fmt.Fprintf(w, "  FAIL after %s: %v\n", res.Elapsed.Round(time.Millisecond), err)
				return
			}
			fmt.Fprintf(w, "  done %s  in=%d out=%d cache=%d  $%.6f\n",
				res.Elapsed.Round(time.Millisecond),
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
