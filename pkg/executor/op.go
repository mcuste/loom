package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// op is the executable behavior of one compiled task body form. It returns the
// task result, a run error from an attempted execution, and a fatal setup error
// detected before the task meaningfully started.
type op interface {
	eval(context.Context, *interpreter, *frame, *node, time.Duration) (TaskResult, error, error)
}

type promptOp struct{}

type shellOp struct{}

type scriptOp struct{}

type subWorkflowOp struct{}

type invalidOp struct{}

func compileOp(t *workflow.Task) op {
	switch t.BodyKind() {
	case workflow.BodyPrompt:
		return promptOp{}
	case workflow.BodyShell:
		return shellOp{}
	case workflow.BodyScript:
		return scriptOp{}
	case workflow.BodySubWorkflow:
		return subWorkflowOp{}
	default:
		return invalidOp{}
	}
}

func (shellOp) eval(ctx context.Context, i *interpreter, st *frame, n *node, baseDelay time.Duration) (TaskResult, error, error) {
	t := n.task
	if i.hooks.OnStart != nil {
		i.hooks.OnStart(*t, st.iteration, "", "", "")
	}
	st.mu.Lock()
	body := workflow.Substitute(bindLoopVar(t.Command, st), st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes)
	env := taskEnv(st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes, st.loopVar, st.loopVal)
	st.mu.Unlock()
	res, runErr := runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
		return runShell(ctx, t, body, env, st.workDir)
	})
	return res, runErr, nil
}

func (scriptOp) eval(ctx context.Context, i *interpreter, st *frame, n *node, baseDelay time.Duration) (TaskResult, error, error) {
	t := n.task
	if i.hooks.OnStart != nil {
		i.hooks.OnStart(*t, st.iteration, "", "", "")
	}
	st.mu.Lock()
	path := workflow.Substitute(bindLoopVar(t.Script, st), st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes)
	args := make([]string, len(t.Args))
	for idx, a := range t.Args {
		args[idx] = workflow.Substitute(bindLoopVar(a, st), st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes)
	}
	env := taskEnv(st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes, st.loopVar, st.loopVal)
	st.mu.Unlock()
	res, runErr := runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
		return runScript(ctx, t, path, args, env, st.workDir)
	})
	return res, runErr, nil
}

func (promptOp) eval(ctx context.Context, i *interpreter, st *frame, n *node, baseDelay time.Duration) (TaskResult, error, error) {
	t := n.task
	wf := i.program.wf
	rt, model, effort := wf.EffectiveWithParams(t, i.opts.Params)
	runner, ok := resolveRunner(i.opts, rt)
	if !ok {
		return TaskResult{}, nil, fmt.Errorf("task %q: runtime %q: %w", t.ID, rt, runtime.ErrUnknownRuntime)
	}
	sysPrompt := workflow.Substitute(wf.EffectiveSystemPrompt(t), nil, i.opts.Params, i.opts.State, nil, nil)
	if i.hooks.OnStart != nil {
		i.hooks.OnStart(*t, st.iteration, rt, model, effort)
	}
	st.mu.Lock()
	body := workflow.Substitute(bindLoopVar(t.Prompt, st), st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes)
	st.mu.Unlock()
	send := func() (TaskResult, error) {
		return runWithRetry(ctx, t, baseDelay, func() (TaskResult, error) {
			r, err := runLLM(ctx, t, body, runner, model, effort, sysPrompt, st.workDir)
			if err != nil {
				return r, err
			}
			// A tolerated non-zero exit produced no structured model output, so
			// schema validation has nothing meaningful to validate.
			if r.ExitCode != 0 {
				return r, nil
			}
			return r, validateSchema(t, r.Output)
		})
	}
	var (
		res    TaskResult
		runErr error
	)
	if i.opts.Cache != nil && wf.CacheEnabled(t) {
		res, runErr = runCached(i.opts.Cache, t, rt, model, effort, sysPrompt, body, send)
	} else {
		res, runErr = send()
	}
	return res, runErr, nil
}

func (invalidOp) eval(_ context.Context, _ *interpreter, _ *frame, n *node, _ time.Duration) (TaskResult, error, error) {
	return TaskResult{}, nil, fmt.Errorf("task %q: invalid body: exactly one of prompt, command, workflow, or script must be set", n.task.ID)
}
