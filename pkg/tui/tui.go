// Package tui is the rendering seam for the loom CLI. It owns every byte the
// commands write to stdout so the plain printer and a future rich renderer can
// be swapped behind one interface without touching cmd/loom's control flow.
package tui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// RunMeta carries the per-run facts the renderer prints in its header block
// before execution: the run-record path, the invocation cwd, the seeded-task
// count (0 on a plain run), and an optional loop-iteration marker. Total is the
// progress denominator for the per-task lines (the expected task count, i.e.
// total tasks minus seeded ones); the Hooks() printer needs it but the executor
// never reports it, so the caller threads it in here.
type RunMeta struct {
	RunFile string
	Cwd     string
	Seeded  int
	Total   int
	Loop    *LoopMeta
}

// LoopMeta is the optional loop-iteration marker on RunMeta. N is 1-based.
type LoopMeta struct {
	N   int
	Max int
}

// Renderer is the rendering seam. A command obtains one via New, prints the
// header, hands Hooks() to the executor for per-task progress, then prints the
// summary and closes. Header must be called before Hooks() so the progress
// denominator (RunMeta.Total) is known. Every method that writes to stdout
// returns the first write error so callers can surface it rather than have it
// vanish into a dropped fmt.Fprintf return.
type Renderer interface {
	// Header prints the per-run header block and records RunMeta.Total as the
	// progress denominator for Hooks(). It also resets the per-task step counter
	// so a fresh iteration's progress restarts at 1. It MUST be called before
	// Hooks().
	Header(meta RunMeta) error
	// Plan prints the validated execution plan: the workflow header, the params
	// table, and the numbered task order.
	Plan(wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string, seeded map[workflow.TaskID]bool) error
	// Warn prints a pre-plan advisory line, prefixed with "warning: ". Routing it
	// through the renderer keeps every stdout byte flowing through the seam rather
	// than around it via a separate writer.
	Warn(msg string) error
	// Hooks returns the executor progress hooks. Header MUST have been called
	// first: the per-task lines read the progress denominator (RunMeta.Total) set
	// by Header, so calling Hooks() first renders every line as [N/0]. The hooks
	// serialize their writes so concurrent task callbacks never interleave
	// mid-line.
	Hooks() executor.Hooks
	// Summary prints the closing totals-and-status block.
	Summary(wf *workflow.Workflow, rep *executor.Report, expected int) error
	// Close releases renderer resources (a rich renderer flushes its display and
	// restores terminal state here) and returns any teardown error.
	Close() error
}

// New returns the renderer for w: the live ttyRenderer when w is an interactive
// terminal (Rich(w)), otherwise the plainRenderer for piped or non-TTY output.
func New(w io.Writer) Renderer {
	if Rich(w) {
		return newTTYRenderer(w)
	}
	return &plainRenderer{w: w}
}

// plainRenderer reproduces cmd/loom's current stdout byte-for-byte. total is the
// progress denominator threaded in via Header; step counts the per-task lines
// and mu serializes concurrent OnStart/OnFinish writes so lines never interleave
// mid-write.
type plainRenderer struct {
	w     io.Writer
	total int
	step  atomic.Int32
	mu    sync.Mutex
}

// errWriter accumulates the first write error so a renderer method can issue a
// run of fmt.Fprintf calls and return a single error at the end instead of
// checking each one. Once err is set, further printf calls are no-ops.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, a ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, a...)
}

// Header prints the optional iteration banner, the run-file/cwd block, and the
// optional seeded line, then records the progress denominator for Hooks() and
// resets the per-task step counter so each iteration's progress restarts at 1
// (a single shared renderer drives every loop iteration).
func (p *plainRenderer) Header(meta RunMeta) error {
	p.total = meta.Total
	p.step.Store(0)
	ew := &errWriter{w: p.w}
	if meta.Loop != nil {
		ew.printf("── iteration %d/%d ──\n\n", meta.Loop.N, meta.Loop.Max)
	}
	ew.printf("Run file : %s\n", meta.RunFile)
	ew.printf("Cwd      : %s\n\n", meta.Cwd)
	if meta.Seeded > 0 {
		ew.printf("Seeded   : %d task(s) from prior run\n\n", meta.Seeded)
	}
	return ew.err
}

// Warn prints a pre-plan advisory line. The "warning: " prefix is added here so
// the wording stays byte-identical to the inlined call this replaced.
func (p *plainRenderer) Warn(msg string) error {
	_, err := fmt.Fprintf(p.w, "warning: %s\n", msg)
	return err
}

// Plan uses workflow.Effective so printed runtime/model/effort match what the
// runtime will actually see. cli drives the per-param provenance tag (cli vs
// default vs MISSING). When seeded is non-empty the section header separates the
// seeded count so a resume plan shows which steps are skipped.
func (p *plainRenderer) Plan(wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string, seeded map[workflow.TaskID]bool) error {
	ew := &errWriter{w: p.w}
	ew.printf("Workflow : %s\n", wf.ID)
	if wf.Description != "" {
		ew.printf("Desc     : %s\n", wf.Description)
	}
	ew.printf("Runtime  : %s\n", orDash(string(wf.Runtime)))
	ew.printf("Model    : %s\n", orDash(string(wf.Model)))
	ew.printf("Effort   : %s\n", orDash(string(wf.Effort)))
	if wf.SystemPrompt != "" {
		ew.printf("System   : %s\n", wf.SystemPrompt)
	}
	if wf.Loop != nil {
		ew.printf("Loop     : until_empty=%s max=%d\n", wf.Loop.UntilEmpty, wf.Loop.Max)
	}

	if len(wf.Params) > 0 {
		nameWidth := 0
		for _, prm := range wf.Params {
			if n := len(prm.Name); n > nameWidth {
				nameWidth = n
			}
		}
		ew.printf("\nParams (%d):\n", len(wf.Params))
		for _, prm := range wf.Params {
			value, ok := resolved[prm.Name]
			source := paramSource(prm, cli, ok)
			if !ok {
				ew.printf("  %-*s = %-12s (%s)\n", nameWidth, prm.Name, "<missing>", source)
				continue
			}
			ew.printf("  %-*s = %-12s (%s)\n", nameWidth, prm.Name, quoteIfNeeded(value), source)
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
		ew.printf("\nExecution order (%d task%s; %d seeded):\n", len(order), plural(len(order)), seedCount)
	} else {
		ew.printf("\nExecution order (%d task%s):\n", len(order), plural(len(order)))
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
		if t.WritesState != "" {
			suffix += "  writes_state=" + t.WritesState
		}
		if t.IsForEach() {
			if t.ForEachSource != "" {
				suffix += fmt.Sprintf("  for_each=dynamic<-%s as=%s", t.ForEachSource, t.As)
			} else {
				suffix += fmt.Sprintf("  for_each=static[%d] as=%s", len(t.ForEach), t.As)
			}
		}
		if seeded[id] {
			suffix += "  (seeded; using stored output)"
		}
		if t.IsShell() {
			cmd := t.Command
			if len(cmd) > 60 {
				cmd = cmd[:60] + "…"
			}
			ew.printf("  %2d. %-*s  kind=shell  cmd=%q  deps=%s%s\n",
				i+1, idWidth, id, cmd, depsList(t.DependsOn), suffix)
		} else {
			rt, m, e := wf.Effective(t)
			ew.printf("  %2d. %-*s  runtime=%-12s  model=%-8s  effort=%-7s  deps=%s%s\n",
				i+1, idWidth, id, orDash(string(rt)), orDash(string(m)), orDash(string(e)), depsList(t.DependsOn), suffix)
		}
	}
	return ew.err
}

// Hooks serializes concurrent OnStart/OnFinish writes behind a mutex so output
// lines never interleave mid-write. The denominator is RunMeta.Total, set by a
// prior Header call: invoking Hooks() before Header() renders every line as
// [N/0]. The callbacks cannot return errors (executor.Hooks fixes their
// signatures), so write errors on the progress lines are explicitly discarded
// rather than silently dropped.
func (p *plainRenderer) Hooks() executor.Hooks {
	return executor.Hooks{
		// A looped task's start line carries an " iter N" annotation so its
		// per-iteration progress is visible; iter is 0 for a non-looped task,
		// where iterSuffix is empty and the output stays byte-identical.
		OnStart: func(t workflow.Task, iter int, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			n := p.step.Add(1)
			p.mu.Lock()
			defer p.mu.Unlock()
			if t.IsShell() {
				_, _ = fmt.Fprintf(p.w, "[%d/%d] %s (shell)%s\n", n, p.total, t.ID, iterSuffix(iter))
			} else {
				_, _ = fmt.Fprintf(p.w, "[%d/%d] %s (%s/%s%s)%s\n", n, p.total, t.ID, rt, m, effortSuffix(e), iterSuffix(iter))
			}
		},
		OnFinish: func(t workflow.Task, iter int, res executor.TaskResult, err error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if t.IsShell() {
				if err != nil {
					_, _ = fmt.Fprintf(p.w, "  %s FAIL after %s: %v\n", t.ID, res.Elapsed.Round(time.Millisecond), err)
					return
				}
				_, _ = fmt.Fprintf(p.w, "  %s done %s  exit=0\n", t.ID, res.Elapsed.Round(time.Millisecond))
				return
			}
			if err != nil {
				_, _ = fmt.Fprintf(p.w, "  %s FAIL after %s: %v\n", t.ID, res.Elapsed.Round(time.Millisecond), err)
				return
			}
			_, _ = fmt.Fprintf(p.w, "  %s done %s  in=%d out=%d cache=%d  $%.6f\n",
				t.ID, res.Elapsed.Round(time.Millisecond),
				res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CacheReadTokens, res.Usage.TotalCostUSD)
		},
	}
}

// Summary compares len(rep.Tasks) against expected to choose the success or
// partial-failure line. expected is the full task count for `loom run` and the
// non-seeded count for `loom resume`.
func (p *plainRenderer) Summary(wf *workflow.Workflow, rep *executor.Report, expected int) error {
	const bar = "────────────────────────────────────────"
	ew := &errWriter{w: p.w}
	ew.printf("\n%s\n", bar)
	ew.printf("  total tokens : %d in / %d out / %d cache-read\n",
		rep.Usage.InputTokens, rep.Usage.OutputTokens, rep.Usage.CacheReadTokens)
	ew.printf("  total cost   : $%.6f\n", rep.Usage.TotalCostUSD)
	ew.printf("%s\n", bar)
	if len(rep.Tasks) == expected {
		ew.printf("✓ workflow %q complete\n", wf.ID)
	} else {
		ew.printf("✗ workflow %q stopped after %d/%d tasks\n", wf.ID, len(rep.Tasks), expected)
	}
	return ew.err
}

// Close is a no-op for the plain renderer; the rich renderer will flush/tear
// down its display here and may report a teardown error.
func (p *plainRenderer) Close() error { return nil }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// iterSuffix renders the loop-pass annotation appended to a looped task's
// progress line (" iter N"). It is empty for iter 0 (a non-looped task) so
// non-looped output stays byte-identical.
func iterSuffix(iter int) string {
	if iter <= 0 {
		return ""
	}
	return fmt.Sprintf(" iter %d", iter)
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
