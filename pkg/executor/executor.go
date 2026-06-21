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
	"maps"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/mcuste/loom/pkg/runtime"
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
	// Status records the task's terminal disposition. It is StatusSkipped when
	// the task's `when:` expression evaluated false (Output is empty in that
	// case) and StatusOK for a task that ran to completion.
	Status string
	// CacheHit is true when Output was replayed from a memoization cache rather
	// than produced by the runtime this run. A cache hit reports zero Usage.
	CacheHit bool
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

// Terminal task statuses surfaced on TaskResult.Status.
const (
	// StatusOK marks a task that ran to completion.
	StatusOK = "ok"
	// StatusSkipped marks a task whose `when:` expression evaluated false: it
	// produced no output but still closed its gate so downstream tasks proceed.
	StatusSkipped = "skipped"
)

// ShellError reports a non-zero exit from a shell task. The wrapped command's
// stderr is captured verbatim so callers can surface it without re-running
// the process.
type ShellError struct {
	ExitCode int
	Stderr   string
}

// Error includes the exit code and trimmed stderr so a single line conveys
// both why the task failed and what the process said.
func (e *ShellError) Error() string {
	s := strings.TrimSpace(e.Stderr)
	if s == "" {
		return fmt.Sprintf("shell: exit %d", e.ExitCode)
	}
	return fmt.Sprintf("shell: exit %d: %s", e.ExitCode, s)
}

// Report is the aggregate outcome of a Run call.
//
// Tasks lists per-task results in completion order — not Plan order — because
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
type Hooks struct {
	OnStart  func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort)
	OnFinish func(t workflow.Task, res TaskResult, err error)
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
	baseDelay := opts.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = defaultRetryBaseDelay
	}
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
	budgetInFlight := false
	budgetReady := sync.NewCond(&mu)

	g, gctx := errgroup.WithContext(ctx)
	for _, tid := range order {
		if _, seeded := opts.Seed[tid]; seeded {
			continue
		}
		t := wf.ByID(tid)
		g.Go(func() error {
			for _, dep := range t.DependsOn {
				select {
				case <-gates[dep]:
				case <-gctx.Done():
					return gctx.Err()
				}
			}

			// Evaluate the task's `when:` guard once its dependencies have
			// resolved. A false result skips the task: it produces empty output
			// and StatusSkipped, but still closes its gate so downstream tasks
			// proceed. Cond was compiled and validated at load time.
			if t.Cond != nil {
				mu.Lock()
				env := workflow.Env{
					Outputs:   maps.Clone(rep.Outputs),
					Succeeded: maps.Clone(succeeded),
					Skipped:   maps.Clone(skipped),
				}
				mu.Unlock()
				run, err := t.Cond.Eval(env)
				if err != nil {
					return fmt.Errorf("task %q: when: %w", t.ID, err)
				}
				if !run {
					res := TaskResult{TaskID: t.ID, Status: StatusSkipped}
					if hooks.OnFinish != nil {
						hooks.OnFinish(*t, res, nil)
					}
					mu.Lock()
					rep.Outputs[t.ID] = ""
					skipped[t.ID] = true
					rep.Tasks = append(rep.Tasks, res)
					mu.Unlock()
					close(gates[t.ID])
					return nil
				}
			}

			// Enforce the workflow cost budget BEFORE dispatching: once the
			// cumulative cost of already-completed tasks exceeds the limit, abort
			// rather than start another task. Spend strictly greater than the
			// limit is "exceeded"; landing exactly on it is allowed.
			if wf.Budget != nil {
				// Wait until no other budget-gated task is in flight, then check and
				// claim the slot under the same lock. This makes the check-then-commit
				// atomic: the in-flight task's cost is recorded (and the slot
				// released) before the next task is admitted, so concurrent subgraphs
				// cannot each read a stale spend and collectively overshoot the limit.
				mu.Lock()
				for budgetInFlight {
					budgetReady.Wait()
					// A wake may come from a sibling's cancellation rather than a slot
					// release; bail without claiming the slot so we never block g.Wait.
					if gctx.Err() != nil {
						mu.Unlock()
						return gctx.Err()
					}
				}
				spent := rep.Usage.TotalCostUSD
				if spent > wf.Budget.MaxCostUSD {
					// Wake peers so they re-evaluate and drain (each will also abort)
					// rather than block forever on the slot this goroutine never takes.
					budgetReady.Broadcast()
					mu.Unlock()
					return &BudgetExceededError{Limit: wf.Budget.MaxCostUSD, Spent: spent}
				}
				budgetInFlight = true
				mu.Unlock()
				// Release the slot once this task returns (after its cost is recorded
				// on the success path, or immediately on a dispatch error), waking the
				// next waiter.
				defer func() {
					mu.Lock()
					budgetInFlight = false
					budgetReady.Broadcast()
					mu.Unlock()
				}()
			}

			var (
				res    TaskResult
				runErr error
			)

			// A for_each task resolves its list and substitutes per instance
			// inside runForEach, so no single body is computed up front; a plain
			// task substitutes its body once here (mu guards rep.Outputs;
			// opts.Params is read-only after Run starts).
			if t.IsShell() {
				if hooks.OnStart != nil {
					hooks.OnStart(*t, "", "", "")
				}
				if t.IsForEach() {
					res, runErr = runForEach(gctx, t, &mu, rep.Outputs, opts, baseDelay, nil, "", "", "")
				} else {
					mu.Lock()
					body := workflow.Substitute(t.Command, rep.Outputs, opts.Params, opts.State)
					mu.Unlock()
					res, runErr = runWithRetry(gctx, t, baseDelay, func() (TaskResult, error) {
						return runShell(gctx, t, body)
					})
				}
			} else {
				rt, model, effort := wf.Effective(t)
				runner, ok := runtime.Lookup(rt)
				if !ok {
					return fmt.Errorf("task %q: runtime %q: %w", t.ID, rt, runtime.ErrUnknownRuntime)
				}
				sysPrompt := workflow.Substitute(wf.SystemPrompt, nil, opts.Params, opts.State)
				if hooks.OnStart != nil {
					hooks.OnStart(*t, rt, model, effort)
				}
				if t.IsForEach() {
					// Memoization keys on a single substituted prompt; a for_each task
					// has one body per instance, so caching it is unsupported. Surface
					// the unsupported combination rather than silently ignoring the
					// annotation.
					if opts.Cache != nil && wf.CacheEnabled(t) {
						return fmt.Errorf("task %q: cache is not supported on for_each tasks", t.ID)
					}
					res, runErr = runForEach(gctx, t, &mu, rep.Outputs, opts, baseDelay, runner, model, effort, sysPrompt)
				} else {
					mu.Lock()
					body := workflow.Substitute(t.Prompt, rep.Outputs, opts.Params, opts.State)
					mu.Unlock()
					dispatch := func() (TaskResult, error) {
						return runWithRetry(gctx, t, baseDelay, func() (TaskResult, error) {
							r, err := runLLM(gctx, t, body, runner, model, effort, sysPrompt)
							if err != nil {
								return r, err
							}
							return r, validateSchema(t, r.Output)
						})
					}
					if opts.Cache != nil && wf.CacheEnabled(t) {
						res, runErr = runCached(opts.Cache, t, rt, model, effort, sysPrompt, body, dispatch)
					} else {
						res, runErr = dispatch()
					}
				}
			}

			if hooks.OnFinish != nil {
				hooks.OnFinish(*t, res, runErr)
			}
			if runErr != nil {
				// A task error aborts the run: the gate is left unclosed, errgroup
				// cancels gctx, and downstream goroutines exit at their <-gctx.Done()
				// wait before ever reaching their own when: evaluation. Consequently
				// failed(id) cannot observe a runtime failure of id in a live run
				// (it is reachable-true only for a never-succeeded, never-skipped
				// disposition, which a future continue-on-error mode would produce).
				// TestRun_WhenFailedDepAbortsRun pins this behavior.
				return fmt.Errorf("task %q: %w", t.ID, runErr)
			}

			res.Status = StatusOK
			mu.Lock()
			rep.Outputs[t.ID] = res.Output
			succeeded[t.ID] = true
			rep.Tasks = append(rep.Tasks, res)
			rep.Usage.InputTokens += res.Usage.InputTokens
			rep.Usage.OutputTokens += res.Usage.OutputTokens
			rep.Usage.CacheReadTokens += res.Usage.CacheReadTokens
			rep.Usage.TotalCostUSD += res.Usage.TotalCostUSD
			mu.Unlock()

			close(gates[t.ID])
			return nil
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
		OnStart: func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort) {
			for _, h := range hs {
				if h.OnStart != nil {
					h.OnStart(t, rt, m, e)
				}
			}
		},
		OnFinish: func(t workflow.Task, res TaskResult, err error) {
			for _, h := range hs {
				if h.OnFinish != nil {
					h.OnFinish(t, res, err)
				}
			}
		},
	}
}
