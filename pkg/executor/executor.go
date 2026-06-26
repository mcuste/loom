// Package executor orchestrates a parsed [workflow.Workflow] against the
// runtimes registered in the [runtime] package. It computes the deterministic
// execution order via [workflow.Workflow.Plan], substitutes each task's
// `{{id}}` and `{{params.name}}` placeholders with upstream task outputs and
// resolved parameter values respectively, and dispatches independent tasks
// concurrently against their registered runtimes. Hooks let callers observe
// per-task progress without coupling the orchestrator to a particular output
// sink.
package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/task"
	"github.com/mcuste/loom/pkg/workflow"
)

// TaskResult is the outcome of one task execution.
//
// For an LLM task, Prompt is the final text sent to the runtime, with `{{id}}`
// and `{{params.name}}` placeholders already substituted, and Command is
// empty. For a shell task, Command holds the substituted command line, Prompt
// is empty, and Usage is zero. Output is the runtime's response in the LLM
// case and the trimmed stdout in the shell case. Persisting these fields lets
// callers reconstruct exactly what the runtime (or shell) saw without
// re-running substitution.
type TaskResult struct {
	TaskID  workflow.TaskID
	Prompt  string
	Command string
	Output  string
	Usage   runtime.Usage
	Elapsed time.Duration
	// Status records the task's terminal disposition. It is task.StatusSkipped
	// when the task's `when:` expression evaluated false (Output is empty in that
	// case) and task.StatusOK for a task that ran to completion.
	Status task.Status
	// CacheHit is true when Output was replayed from a memoization cache rather
	// than produced by the runtime this run. A cache hit reports zero Usage.
	CacheHit bool
	// Iteration is the 1-based loop pass that produced this result for a task
	// that is a member of a scoped loop, and 0 for a non-looped task. It lets
	// callers attribute a result to the pass that generated it.
	Iteration int
}

// Cache memoizes LLM task outputs across runs. The executor consults it before
// dispatching a cacheable LLM task: on a hit it replays the stored output with
// zero usage and skips the runtime; on a miss it runs the task and records the
// output. A nil Options.Cache disables memoization. Shell tasks are never
// memoized. Implementations must be safe for concurrent use.
type Cache interface {
	// Lookup returns the memoized output for the given task inputs and true when
	// a prior run cached it, or ("", false, nil) on a miss.
	Lookup(rt runtime.Name, model runtime.Model, effort runtime.Effort, systemPrompt, prompt string) (string, bool, error)
	// Save records output for the given task inputs.
	Save(rt runtime.Name, model runtime.Model, effort runtime.Effort, systemPrompt, prompt, output string) error
}

// Terminal task statuses surfaced on TaskResult.Status. These re-export the
// canonical values from [task] so the string literals live in exactly one
// place; existing executor.StatusOK / executor.StatusSkipped references resolve
// unchanged.
const (
	StatusOK      = task.StatusOK
	StatusSkipped = task.StatusSkipped
)

// ShellError reports a non-zero exit from a shell task. The wrapped command's
// stderr is captured verbatim so callers can surface it without re-running
// the process.
type ShellError struct {
	ExitCode int
	Stderr   string
}

// Error conveys both why the task failed and what the process said, so a caller
// can surface the failure without re-running the command.
func (e *ShellError) Error() string {
	s := strings.TrimSpace(e.Stderr)
	if s == "" {
		return fmt.Sprintf("shell: exit %d", e.ExitCode)
	}
	return fmt.Sprintf("shell: exit %d: %s", e.ExitCode, s)
}

// Report is the aggregate outcome of a Run call.
//
// Tasks lists per-task results in completion order (not Plan order) because
// independent tasks run concurrently and finish at different times. Outputs is
// the same data keyed by id for placeholder lookups. Usage sums Usage across
// every completed task. Params carries opts.Params verbatim so callers can
// read what actually substituted without re-resolving. On partial failure (Run
// returns non-nil error), the report contains the tasks that completed before
// the failure.
type Report struct {
	Tasks   []TaskResult
	Outputs map[workflow.TaskID]string
	Usage   runtime.Usage
	Params  workflow.ParamValues
}

// Hooks let callers observe per-task progress without owning an output sink.
// Either field may be nil. OnStart fires once per task before Run is called
// on the runtime; OnFinish fires after Run returns, regardless of success
// (err is nil on success). Under concurrent execution, hook calls for
// different tasks may interleave; implementations must be safe for
// concurrent use.
//
// Consumers MUST use t.IsShell() to distinguish shell tasks from LLM tasks.
// For a shell task, OnStart is invoked with empty runtime.Name, runtime.Model,
// and runtime.Effort because those routing fields genuinely do not apply, but
// their emptiness is not the contract: do not infer shell-ness from it.
//
// iter is the 1-based scoped-loop pass that produced the event and 0 for a
// non-looped task, letting observers attribute a start/finish to the iteration
// that generated it without inspecting TaskResult.
type Hooks struct {
	OnStart  func(t workflow.Task, iter int, rt runtime.Name, m runtime.Model, e runtime.Effort)
	OnFinish func(t workflow.Task, iter int, res TaskResult, err error)
}

// Options configures a Run call. The zero value is valid and runs the workflow
// with no parameters.
type Options struct {
	// Params holds resolved parameter values passed via --param flags. Read-only
	// after Run starts: goroutines read it without the lock.
	Params workflow.ParamValues
	// State holds the cross-run state map (key → value) consulted for
	// `{{state.key}}` substitution. Read-only, like Params: the executor never
	// writes it. A missing key substitutes to empty string. May be nil.
	State map[string]string
	// Seed maps task ids to outputs that should be treated as already-produced.
	// Seeded tasks have their gates closed with the supplied value before any
	// goroutine launches, so unseeded downstream tasks see the seed via
	// {{id}} substitution. Seeded tasks fire no hooks and do not appear in
	// Report.Tasks. Entries naming an id not in the workflow are ignored.
	Seed map[workflow.TaskID]string
	// RetryBaseDelay is the base backoff delay between retry attempts. When
	// zero, defaultRetryBaseDelay (1s) applies. Carried per-call rather than as
	// package state so concurrent Run calls (and parallel tests) never share a
	// mutable delay.
	RetryBaseDelay time.Duration
	// Cache, when non-nil, memoizes the output of cacheable LLM tasks: the
	// executor replays a stored output on a hit and records a fresh one on a
	// miss. nil disables memoization. Shell tasks are never memoized.
	Cache Cache
}

// Run executes wf's tasks concurrently, respecting the dependency graph.
// Each task waits for its upstream dependencies to complete, substitutes
// `{{id}}` and `{{params.name}}` placeholders in its prompt (or command) with
// upstream outputs and opts.Params respectively, then dispatches either a
// single [runtime.Request] to its registered runtime or, for shell tasks, a
// `sh -c` child process.
//
// On the first task error, sibling goroutines are cancelled via context and
// Run returns the partial Report along with the wrapped error. Cancellation
// of the caller's ctx propagates the same way.
func Run(ctx context.Context, wf *workflow.Workflow, hooks Hooks, opts Options) (*Report, error) {
	order := wf.Plan()
	rep := &Report{
		Tasks:   make([]TaskResult, 0, len(order)),
		Outputs: make(map[workflow.TaskID]string, len(order)),
		Params:  opts.Params, // stash once before any g.Go; goroutines only read it
	}

	gates := make(map[workflow.TaskID]chan struct{}, len(order))
	for _, tid := range order {
		gates[tid] = make(chan struct{})
	}

	// succeeded records, per completed task, whether it ran to completion;
	// skipped records, per task, whether its `when:` guard skipped it. The two
	// are distinct so failed()/succeeded() never conflate a skip with a failure.
	// Both are read under mu like rep.Outputs.
	succeeded := make(map[workflow.TaskID]bool, len(order))
	skipped := make(map[workflow.TaskID]bool, len(order))

	// Close seeded gates and stamp their outputs before spawning any
	// goroutine. Downstream waiters see the seed via {{id}} substitution
	// just as if the task had run this invocation. Unknown ids are ignored.
	for _, tid := range order {
		if v, ok := opts.Seed[tid]; ok {
			rep.Outputs[tid] = v
			succeeded[tid] = true
			close(gates[tid])
		}
	}

	var mu sync.Mutex

	// budgetInFlight serializes budget-gated dispatches so the check-then-commit
	// is atomic. A workflow budget is enforced against the cumulative cost of
	// *completed* tasks, but a task's cost is only known after it runs; without
	// serialization, two goroutines whose deps resolve concurrently both read a
	// stale spend, both pass the check, and the concurrent subgraph overshoots
	// the limit. Admitting at most one budget-gated task at a time guarantees its
	// cost is recorded before the next task's check runs, bounding the overshoot
	// to the single task that crosses the limit (matching the serial semantics).
	// budgetReady wakes a waiter once the in-flight task records its cost.
	st := &runState{
		rep:            rep,
		outputs:        rep.Outputs,
		succeeded:      succeeded,
		skipped:        skipped,
		gates:          gates,
		mu:             &mu,
		budgetInFlight: new(bool),
		budgetReady:    sync.NewCond(&mu),
	}

	// Each scoped loop collapses its body into a single synthetic node in the
	// outer schedule: the body's member tasks are not scheduled individually
	// here (a per-loop driver runs them iteratively), and an outside task that
	// depends on a member waits on that member's gate, which the driver closes
	// only once the loop converges. memberOf records which loop owns each body
	// task so the scheduler can skip it.
	memberOf := make(map[workflow.TaskID]int, len(wf.Loops))
	for i := range wf.Loops {
		for _, m := range wf.Loops[i].Members {
			memberOf[m] = i
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	for i := range wf.Loops {
		lg := &wf.Loops[i]
		g.Go(func() error {
			return runLoop(gctx, wf, lg, st, hooks, opts)
		})
	}
	for _, tid := range order {
		if _, seeded := opts.Seed[tid]; seeded {
			continue
		}
		if _, looped := memberOf[tid]; looped {
			continue
		}
		t := wf.ByID(tid)
		g.Go(func() error {
			return runTask(gctx, wf, t, st, hooks, opts)
		})
	}

	err := g.Wait()
	return rep, err
}

// runCached wraps an LLM dispatch with hash-based memoization. It consults the
// cache for the task's inputs first: on a hit it replays the stored output with
// zero usage and the CacheHit marker, skipping the runtime entirely; on a miss
// it dispatches and records the produced output for the next run. A failed
// dispatch is returned as-is and never cached.
func runCached(cache Cache, t *workflow.Task, rt runtime.Name, model runtime.Model, effort runtime.Effort, sysPrompt, prompt string, dispatch func() (TaskResult, error)) (TaskResult, error) {
	out, hit, err := cache.Lookup(rt, model, effort, sysPrompt, prompt)
	if err != nil {
		return TaskResult{TaskID: t.ID, Prompt: prompt}, fmt.Errorf("cache lookup: %w", err)
	}
	if hit {
		return TaskResult{TaskID: t.ID, Prompt: prompt, Output: out, CacheHit: true}, nil
	}
	res, err := dispatch()
	if err != nil {
		return res, err
	}
	// Cache persistence is best-effort: a successful LLM call must not be turned
	// into a task failure by a transient write error (disk full, permissions).
	// The next run simply re-computes on the resulting miss.
	_ = cache.Save(rt, model, effort, sysPrompt, prompt, res.Output)
	return res, nil
}

// runLLM executes one LLM task against its resolved runner. The substituted
// prompt and system prompt are passed in by the dispatcher so this helper has
// no awareness of the surrounding workflow.
func runLLM(ctx context.Context, t *workflow.Task, prompt string, runner runtime.Runner, model runtime.Model, effort runtime.Effort, sysPrompt string) (TaskResult, error) {
	req := runtime.Request{
		TaskID:       string(t.ID),
		Prompt:       prompt,
		Model:        model,
		Effort:       effort,
		SystemPrompt: sysPrompt,
	}

	start := time.Now()
	resp, runErr := runner.Run(ctx, req)
	res := TaskResult{
		TaskID:  t.ID,
		Prompt:  prompt,
		Output:  resp.Output,
		Usage:   resp.Usage,
		Elapsed: time.Since(start),
	}
	return res, runErr
}

// runShell executes one shell task as `sh -c <line>`. The provided ctx cancels
// the child process on Run-level cancellation or sibling failure. Stdout is
// captured and trimmed of trailing newlines; stderr is captured verbatim and
// surfaced on non-zero exit via [ShellError].
func runShell(ctx context.Context, t *workflow.Task, line string) (TaskResult, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	res := TaskResult{
		TaskID:  t.ID,
		Command: line,
		Output:  strings.TrimRight(string(out), "\n"),
		Elapsed: time.Since(start),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return res, &ShellError{ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
		}
		return res, err
	}
	return res, nil
}

// JoinHooks fans an event out to every hook set in registration order, so
// independent observers (printer, store, telemetry) can be layered without
// coupling their implementations. Nil function fields in any set are skipped.
func JoinHooks(hs ...Hooks) Hooks {
	return Hooks{
		OnStart: func(t workflow.Task, iter int, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			for _, h := range hs {
				if h.OnStart != nil {
					h.OnStart(t, iter, rt, m, e)
				}
			}
		},
		OnFinish: func(t workflow.Task, iter int, res TaskResult, err error) {
			for _, h := range hs {
				if h.OnFinish != nil {
					h.OnFinish(t, iter, res, err)
				}
			}
		},
	}
}
