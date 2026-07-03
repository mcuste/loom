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
	"github.com/mcuste/loom/pkg/run"
	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// Renderer is the rendering seam. A command obtains one via New, prints the
// header, hands Hooks() to the executor for per-task progress, then prints the
// summary and closes. Header must be called before Hooks() so the progress
// denominator (runner.RunMeta.Total) is known. Every method that writes to
// stdout returns the first write error so callers can surface it rather than
// have it vanish into a dropped fmt.Fprintf return.
type Renderer interface {
	// Header prints the per-run header block and records runner.RunMeta.Total as
	// the progress denominator for Hooks(). It also resets the per-task step
	// counter so a fresh iteration's progress restarts at 1. It MUST be called
	// before Hooks().
	Header(meta runner.RunMeta) error
	// Plan prints the validated execution plan: the workflow header, the params
	// table, and the numbered task order.
	Plan(wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string, seeded map[workflow.TaskID]bool) error
	// Warn prints a pre-plan advisory line, prefixed with "warning: ". Routing it
	// through the renderer keeps every stdout byte flowing through the seam rather
	// than around it via a separate writer.
	Warn(msg string) error
	// Events returns the run event sink consumed by the runner. Header MUST have
	// been called first: the per-task lines read the progress denominator
	// (runner.RunMeta.Total) set by Header. The sink serializes writes so
	// concurrent task events never interleave mid-line.
	Events() run.EventSink
	// Hooks returns the executor progress hooks for tests and lower-level
	// adapters that still exercise the renderer at the callback seam.
	Hooks() executor.Hooks
	// Summary prints the closing totals-and-status block.
	Summary(wf *workflow.Workflow, rep *executor.Report, expected int) error
	// StoreError reports a best-effort store-layer failure on its own indented
	// line. It has no error channel of its own: the run's real error already
	// won, so write failures here are intentionally discarded.
	StoreError(err error)
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

// Header prints the run-file/cwd block and the optional seeded line, then
// records the progress denominator for Hooks() and resets the per-task step
// counter so progress starts at 1.
func (p *plainRenderer) Header(meta runner.RunMeta) error {
	p.total = meta.Total
	p.step.Store(0)
	ew := &errWriter{w: p.w}
	ew.printf("Run file : %s\n", meta.RunFile)
	ew.printf("Cwd      : %s\n\n", meta.Cwd)
	if meta.Seeded > 0 {
		ew.printf("Seeded   : %d task(s) from prior run\n\n", meta.Seeded)
	}
	return ew.err
}

// Warn prints a pre-plan advisory line. The "warning: " prefix is added here,
// not by the caller, to keep plain-output bytes stable across callers.
func (p *plainRenderer) Warn(msg string) error {
	_, err := fmt.Fprintf(p.w, "warning: %s\n", msg)
	return err
}

// Plan uses workflow.EffectiveWithParams so printed runtime/model/effort match
// what the runtime will actually see. cli drives the per-param provenance tag
// (cli vs default vs MISSING). When seeded is non-empty the section header
// separates the seeded count so a resume plan shows which steps are skipped.
func (p *plainRenderer) Plan(wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string, seeded map[workflow.TaskID]bool) error {
	ew := &errWriter{w: p.w}
	emitPlainWorkflowHeader(ew, wf, resolved)
	emitPlainParams(ew, wf, resolved, cli)

	layout := buildPlanLayout(wf, seeded)
	emitPlainExecutionHeader(ew, layout)

	for step, flow := range layout.flow {
		if flow.loopIndex >= 0 {
			emitPlainLoopStep(ew, wf, layout, flow.loopIndex, step+1, resolved)
			continue
		}
		emitPlainTaskStep(ew, wf, layout, flow.taskID, step+1, resolved, seeded)
	}
	return ew.err
}

func emitPlainWorkflowHeader(ew *errWriter, wf *workflow.Workflow, resolved workflow.ParamValues) {
	ew.printf("Workflow : %s\n", wf.ID)
	if wf.Description != "" {
		ew.printf("Desc     : %s\n", wf.Description)
	}
	rt, model, effort := wf.EffectiveWithParams(&workflow.Task{}, resolved)
	ew.printf("Runtime  : %s\n", orDash(string(rt)))
	ew.printf("Model    : %s\n", orDash(string(model)))
	ew.printf("Effort   : %s\n", orDash(string(effort)))
	if wf.WorkingDir != "" {
		ew.printf("WorkDir  : %s\n", wf.WorkingDir)
	}
	if wf.SystemPrompt != "" {
		ew.printf("System   : %s\n", wf.SystemPrompt)
	}
}

func emitPlainParams(ew *errWriter, wf *workflow.Workflow, resolved workflow.ParamValues, cli map[string]string) {
	if len(wf.Params) == 0 {
		return
	}
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

func emitPlainExecutionHeader(ew *errWriter, layout planLayout) {
	counts := fmt.Sprintf("%d task%s", layout.topLevelTaskCount, plural(layout.topLevelTaskCount))
	if layout.loopCount > 0 {
		counts += fmt.Sprintf(", %d loop%s", layout.loopCount, plural(layout.loopCount))
	}
	if layout.seededTaskCount > 0 {
		ew.printf("\nExecution order (%s; %d seeded):\n", counts, layout.seededTaskCount)
		return
	}
	ew.printf("\nExecution order (%s):\n", counts)
}

func emitPlainTaskStep(ew *errWriter, wf *workflow.Workflow, layout planLayout, id workflow.TaskID, step int, resolved workflow.ParamValues, seeded map[workflow.TaskID]bool) {
	t := wf.ByID(id)
	suffix := ""
	if t.WritesState != "" {
		suffix += "  writes_state=" + t.WritesState
	}
	if seeded[id] {
		suffix += "  (seeded; using stored output)"
	}
	ew.printf("  %2d. %-*s  %s  deps=%s%s\n",
		step, layout.flowIDWidth, id, planTaskCols(wf, t, resolved), depsList(t.DependsOn), suffix)
	if !t.IsSubWorkflow() {
		return
	}
	if child := wf.Subs[id]; child != nil {
		cw := childIDWidth(child)
		for i := range child.Tasks {
			ct := &child.Tasks[i]
			ew.printf("      - %-*s  %s  deps=%s\n",
				cw, ct.ID, planTaskCols(child, ct, nil), depsList(ct.DependsOn))
		}
	}
}

func emitPlainLoopStep(ew *errWriter, wf *workflow.Workflow, layout planLayout, loopIndex, step int, resolved workflow.ParamValues) {
	lg := &wf.Loops[loopIndex]
	ew.printf("  %2d. %-*s  loop  %s\n",
		step, layout.flowIDWidth, lg.ID, loopDescriptor(*lg))
	if lg.Description != "" {
		ew.printf("      desc: %s\n", lg.Description)
	}
	bodyWidth := layout.loopBodyIDWidth[lg.ID]
	for _, id := range lg.Members {
		t := wf.ByID(id)
		ew.printf("      - %-*s  %s  deps=%s\n",
			bodyWidth, id, planTaskCols(wf, t, resolved), depsList(t.DependsOn))
	}
}

// loopConvergence renders a while loop's convergence target as
// until_empty=<task> or until=<expr>, matching whichever field the loop uses.
func loopConvergence(lg workflow.LoopGroup) string {
	if lg.UntilEmpty != "" {
		return "until_empty=" + string(lg.UntilEmpty)
	}
	return "until=" + lg.Until
}

// loopDescriptor renders a scoped loop's kind-specific summary: a while loop
// shows its convergence target and iteration cap; a for_each loop shows its
// loop variable and list source (static `[n]` or dynamic `<-{{src}}`), with the
// label reading `for_each_parallel` when its passes run concurrently.
func loopDescriptor(lg workflow.LoopGroup) string {
	if lg.Kind == workflow.LoopForEach {
		src := fmt.Sprintf("static[%d]", len(lg.List))
		if lg.ListSource != "" {
			src = "dynamic<-" + lg.ListSource
		}
		label := "for_each"
		if lg.Parallel {
			label = "for_each_parallel"
		}
		return fmt.Sprintf("%s  as=%s  in=%s", label, lg.As, src)
	}
	return fmt.Sprintf("%s  max=%d", loopConvergence(lg), lg.Max)
}

// subworkflowDescriptor renders a sub-workflow task's plan summary: the linked
// ref and, once the child is resolved into wf.Subs, its direct task count.
func subworkflowDescriptor(wf *workflow.Workflow, t *workflow.Task) string {
	if child := wf.Subs[t.ID]; child != nil {
		return fmt.Sprintf("workflow=%s  (%d task%s)", t.Workflow, len(child.Tasks), plural(len(child.Tasks)))
	}
	return fmt.Sprintf("workflow=%s", t.Workflow)
}

// exitNote renders a trailing "  exit=N" annotation for a non-zero exit code,
// and "" for the common zero case. An LLM task only carries a non-zero code when
// its ok_exit tolerated a runtime failure, so the note surfaces that without
// cluttering the normal success line.
func exitNote(code int) string {
	if code == 0 {
		return ""
	}
	return fmt.Sprintf("  exit=%d", code)
}

// planTaskCols renders the kind-specific middle columns of a plan row (between
// the id and the trailing deps): a shell task shows its command, a sub-workflow
// task its linked ref and child-task count, an LLM task its effective
// runtime/model/effort triple.
func planTaskCols(wf *workflow.Workflow, t *workflow.Task, resolved workflow.ParamValues) string {
	switch t.BodyKind() {
	case workflow.BodyShell:
		cmd := t.Command
		if len(cmd) > 60 {
			cmd = cmd[:60] + "…"
		}
		return fmt.Sprintf("kind=shell  cmd=%q", cmd)
	case workflow.BodyScript:
		script := t.Script
		if len(t.Args) > 0 {
			script += " " + strings.Join(t.Args, " ")
		}
		if len(script) > 60 {
			script = script[:60] + "…"
		}
		return fmt.Sprintf("kind=script  exec=%q", script)
	case workflow.BodySubWorkflow:
		return "kind=subworkflow  " + subworkflowDescriptor(wf, t)
	case workflow.BodyPrompt:
		rt, m, e := wf.EffectiveWithParams(t, resolved)
		return fmt.Sprintf("runtime=%-12s  model=%-8s  effort=%-7s", orDash(string(rt)), orDash(string(m)), orDash(string(e)))
	default:
		// BodyInvalid: a hand-built or corrupted task that set none or more than
		// one body form. Surface it in the plan rather than silently rendering it
		// as an LLM task, matching the executor's fail-fast on the same shape.
		return "kind=INVALID  (exactly one of prompt, command, workflow, or script must be set)"
	}
}

// childIDWidth returns the widest task id among a resolved child workflow's
// direct tasks, for aligning the indented child rows under a sub-workflow entry.
func childIDWidth(child *workflow.Workflow) int {
	w := 0
	for i := range child.Tasks {
		if n := len(child.Tasks[i].ID); n > w {
			w = n
		}
	}
	return w
}

// Events adapts the renderer's callback implementation to the run event seam.
func (p *plainRenderer) Events() run.EventSink {
	return run.SinkFromHooks(p.Hooks())
}

// Events adapts the renderer's callback implementation to the run event seam.
func (t *ttyRenderer) Events() run.EventSink {
	return run.SinkFromHooks(t.Hooks())
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
			// A for_each fans one task node into many passes; total counts the node
			// once (the iter-1 pass), so each extra pass (iter >= 2) grows the
			// denominator to keep [n/total] honest as the loop expands.
			if iter >= 2 {
				p.total++
			}
			switch {
			case t.IsShell():
				_, _ = fmt.Fprintf(p.w, "[%d/%d] %s (shell)%s\n", n, p.total, t.ID, iterSuffix(iter))
			case t.IsScript():
				_, _ = fmt.Fprintf(p.w, "[%d/%d] %s (script)%s\n", n, p.total, t.ID, iterSuffix(iter))
			case t.IsSubWorkflow():
				_, _ = fmt.Fprintf(p.w, "[%d/%d] %s (subworkflow %s)%s\n", n, p.total, t.ID, t.Workflow, iterSuffix(iter))
			default:
				_, _ = fmt.Fprintf(p.w, "[%d/%d] %s (%s/%s%s)%s\n", n, p.total, t.ID, rt, m, effortSuffix(e), iterSuffix(iter))
			}
		},
		OnFinish: func(t workflow.Task, iter int, res executor.TaskResult, err error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if err != nil {
				_, _ = fmt.Fprintf(p.w, "  %s FAIL after %s: %v\n", t.ID, res.Elapsed.Round(time.Millisecond), err)
				return
			}
			if t.IsShell() || t.IsScript() {
				// Show the real exit code: a command/script with ok_exit can succeed
				// with a non-zero code.
				_, _ = fmt.Fprintf(p.w, "  %s done %s  exit=%d\n", t.ID, res.Elapsed.Round(time.Millisecond), res.ExitCode)
				return
			}
			_, _ = fmt.Fprintf(p.w, "  %s done %s  in=%d out=%d cache=%d  $%.6f%s\n",
				t.ID, res.Elapsed.Round(time.Millisecond),
				res.Usage.InputTokens, res.Usage.OutputTokens, res.Usage.CacheReadTokens, res.Usage.TotalCostUSD,
				exitNote(res.ExitCode))
		},
	}
}

// Summary compares the number of distinct tasks that ran against expected to
// choose the success or partial-failure line. expected is the full task count
// for `loom run` and the non-seeded count for `loom resume`. Distinct (not raw)
// entries are counted because a scoped loop or a for_each fan-out records
// multiple rep.Tasks entries per task id; raw length would exceed expected on
// every looping run and mislabel a success as a partial stop.
func (p *plainRenderer) Summary(wf *workflow.Workflow, rep *executor.Report, expected int) error {
	const bar = "────────────────────────────────────────"
	ew := &errWriter{w: p.w}
	ew.printf("\n%s\n", bar)
	ew.printf("  total tokens : %d in / %d out / %d cache-read\n",
		rep.Usage.InputTokens, rep.Usage.OutputTokens, rep.Usage.CacheReadTokens)
	ew.printf("  total cost   : $%.6f\n", rep.Usage.TotalCostUSD)
	ew.printf("%s\n", bar)
	done := distinctTasks(rep)
	if done == expected {
		ew.printf("✓ workflow %q complete\n", wf.ID)
	} else {
		ew.printf("✗ workflow %q stopped after %d/%d tasks\n", wf.ID, done, expected)
	}
	return ew.err
}

// StoreError writes a store-layer error on its own indented line. The store is
// best-effort relative to the run itself, so a failed write is intentionally
// discarded.
func (p *plainRenderer) StoreError(err error) {
	_, _ = fmt.Fprintf(p.w, "  store: %v\n", err)
}

// distinctTasks counts unique task ids across rep.Tasks. A failed run omits the
// failing task (and everything downstream) from the report, so a distinct count
// below expected marks a partial stop; a loop or for_each that completes
// collapses its repeated entries to one per id and reaches expected.
func distinctTasks(rep *executor.Report) int {
	seen := make(map[workflow.TaskID]struct{}, len(rep.Tasks))
	for _, r := range rep.Tasks {
		seen[r.TaskID] = struct{}{}
	}
	return len(seen)
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

// paramSource returns the provenance tag for p: "cli" if the name appears in
// cli, "default" if resolvedHasValue is true, "MISSING" otherwise. The return
// value is consumed by paramTag to render the colored provenance label.
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
