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
}

// ShellError reports a non-zero exit from a shell task. The wrapped command's
// stderr is captured verbatim so callers can surface it without re-running
// the process.
type ShellError struct {
	ExitCode int
	Stderr   string
}

// Error implements error. Includes the exit code and the trimmed stderr so a
// single line conveys both why the task failed and what the process said.
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
	Params  workflow.ParamValues // carries opts.Params verbatim; nil when no params were used.
}

// Hooks let callers observe per-task progress without owning an output sink.
// Either field may be nil. OnStart fires once per task before Run is called
// on the runtime; OnFinish fires after Run returns, regardless of success
// (err is nil on success). Under concurrent execution, hook calls for
// different tasks may interleave; implementations must be safe for
// concurrent use.
//
// For a shell task, OnStart is invoked with empty runtime.Name, runtime.Model,
// and runtime.Effort values — the CLI uses that as the discriminator to render
// a shell-flavored progress line.
type Hooks struct {
	OnStart  func(t workflow.Task, rt runtime.Name, m runtime.Model, e runtime.Effort)
	OnFinish func(t workflow.Task, res TaskResult, err error)
}

// Options configures a Run call. The zero value is valid and runs the workflow
// with no parameters.
type Options struct {
	Params workflow.ParamValues // resolved params; read-only after Run starts.
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

	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	for _, tid := range order {
		t := wf.ByID(tid)
		g.Go(func() error {
			for _, dep := range t.DependsOn {
				select {
				case <-gates[dep]:
				case <-gctx.Done():
					return gctx.Err()
				}
			}

			// Substitute the body (Prompt or Command) for both task kinds before
			// dispatch. mu guards rep.Outputs; opts.Params is read-only after Run
			// starts, no lock needed.
			mu.Lock()
			var body string
			if t.Command != "" {
				body = workflow.Substitute(t.Command, rep.Outputs, opts.Params)
			} else {
				body = workflow.Substitute(t.Prompt, rep.Outputs, opts.Params)
			}
			mu.Unlock()

			var (
				res    TaskResult
				runErr error
			)

			if t.Command != "" {
				if hooks.OnStart != nil {
					hooks.OnStart(*t, "", "", "")
				}
				res, runErr = runShell(gctx, t, body)
			} else {
				rt, model, effort := wf.Effective(t)
				runner, ok := runtime.Lookup(rt)
				if !ok {
					return fmt.Errorf("task %q: runtime %q: %w", t.ID, rt, runtime.ErrUnknownRuntime)
				}
				sysPrompt := workflow.Substitute(wf.SystemPrompt, nil, opts.Params)
				if hooks.OnStart != nil {
					hooks.OnStart(*t, rt, model, effort)
				}
				res, runErr = runLLM(gctx, t, body, runner, model, effort, sysPrompt)
			}

			if hooks.OnFinish != nil {
				hooks.OnFinish(*t, res, runErr)
			}
			if runErr != nil {
				return fmt.Errorf("task %q: %w", t.ID, runErr)
			}

			mu.Lock()
			rep.Outputs[t.ID] = res.Output
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
