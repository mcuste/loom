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
	manifest, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	wf, err := workflow.Parse(manifest)
	if err != nil {
		return err
	}
	printPlan(w, wf)
	fmt.Fprintln(w)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	run, err := store.Open(wf.ID, manifest, store.Config{
		OnError: func(e error) { fmt.Fprintf(w, "  store: %v\n", e) },
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Run file : %s\n\n", run.Path())

	rep, err := executor.Run(ctx, wf, combine(hooks(w, len(wf.Tasks)), executor.Hooks{
		OnStart:  run.OnStart(),
		OnFinish: run.OnFinish(),
	}))
	if closeErr := run.Close(rep, err); closeErr != nil {
		fmt.Fprintf(w, "  store: %v\n", closeErr)
	}
	if rep != nil {
		printSummary(w, wf, rep)
	}
	return err
}

// combine fans an executor event out to multiple hook sets in registration
// order. Used to layer the store's persistence hooks on top of the printer
// hooks without coupling either implementation to the other.
func combine(hs ...executor.Hooks) executor.Hooks {
	return executor.Hooks{
		OnStart: func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			for _, h := range hs {
				if h.OnStart != nil {
					h.OnStart(t, rt, m, e)
				}
			}
		},
		OnFinish: func(t workflow.Task, res executor.TaskResult, err error) {
			for _, h := range hs {
				if h.OnFinish != nil {
					h.OnFinish(t, res, err)
				}
			}
		},
	}
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
		rt, m, e := wf.Effective(t)
		fmt.Fprintf(w, "  %2d. %-*s  runtime=%-12s  model=%-8s  effort=%-7s  deps=%s\n",
			i+1, idWidth, id, orDash(string(rt)), orDash(string(m)), orDash(string(e)), depsList(t.DependsOn))
	}
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
			fmt.Fprintf(w, "[%d/%d] %s (%s/%s%s)\n", n, total, t.ID, rt, m, effortSuffix(e))
			mu.Unlock()
		},
		OnFinish: func(t workflow.Task, res executor.TaskResult, err error) {
			mu.Lock()
			defer mu.Unlock()
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
