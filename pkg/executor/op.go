package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/mcuste/loom/pkg/plan"
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

func renderTemplate(tpl workflow.Template, st *frame, opts Options) string {
	loopVars := map[string]string(nil)
	if st.loopVar != "" {
		// Hand-built workflows may not carry parse-scoped LoopVarRef parts in
		// their compiled templates. Reparse in the active loop scope so they keep
		// the same {{as}} binding behavior as parsed workflows.
		tpl = workflow.ParseTemplateInScope(tpl.String(), st.loopVar)
		loopVars = map[string]string{st.loopVar: st.loopVal}
	}
	return tpl.Render(workflow.RenderContext{
		Outputs:   st.scope.outputs,
		Params:    opts.Params,
		State:     opts.State,
		Prev:      st.prev,
		ExitCodes: st.scope.exitCodes,
		LoopVars:  loopVars,
	})
}

func renderTemplates(values []workflow.Template, st *frame, opts Options) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = renderTemplate(value, st, opts)
	}
	return out
}

func resolveRoutingValue(value string, params workflow.ParamValues) string {
	name, ok := workflow.PlaceholderParamName(value)
	if !ok {
		return value
	}
	if resolved, found := params[workflow.ParamName(name)]; found {
		return resolved
	}
	return value
}

func invalidActionError(n *node, want string) error {
	return fmt.Errorf("task %q: compiled action %T is not %s", n.id(), n.step.Action, want)
}

func cacheEnabled(env runtimeEnv, n *node) bool {
	policy := n.policy()
	if policy.Cache != nil {
		return *policy.Cache
	}
	return env.cacheDefault
}

func (shellOp) eval(ctx context.Context, i *interpreter, st *frame, n *node, baseDelay time.Duration) (TaskResult, error, error) {
	t := n.taskPayload()
	action, ok := n.action().(plan.RunCommand)
	if !ok {
		return TaskResult{}, nil, invalidActionError(n, "shell command")
	}
	if i.hooks.OnStart != nil {
		i.hooks.OnStart(*t, st.iteration, "", "", "")
	}
	st.mu.Lock()
	body := renderTemplate(action.Command, st, i.opts)
	env := taskEnv(st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes, st.loopVar, st.loopVal)
	st.mu.Unlock()
	policy := n.policy()
	res, runErr := runWithRetry(ctx, t.ID, policy.Retry, policy.Budget, baseDelay, func() (TaskResult, error) {
		return runShell(ctx, t, body, env, st.workDir)
	})
	return res, runErr, nil
}

func (scriptOp) eval(ctx context.Context, i *interpreter, st *frame, n *node, baseDelay time.Duration) (TaskResult, error, error) {
	t := n.taskPayload()
	action, ok := n.action().(plan.RunScript)
	if !ok {
		return TaskResult{}, nil, invalidActionError(n, "script")
	}
	if i.hooks.OnStart != nil {
		i.hooks.OnStart(*t, st.iteration, "", "", "")
	}
	st.mu.Lock()
	path, args := renderTemplate(action.Path, st, i.opts), renderTemplates(action.Args, st, i.opts)
	env := taskEnv(st.scope.outputs, i.opts.Params, i.opts.State, st.prev, st.scope.exitCodes, st.loopVar, st.loopVal)
	st.mu.Unlock()
	policy := n.policy()
	res, runErr := runWithRetry(ctx, t.ID, policy.Retry, policy.Budget, baseDelay, func() (TaskResult, error) {
		return runScript(ctx, t, path, args, env, st.workDir)
	})
	return res, runErr, nil
}

func (promptOp) eval(ctx context.Context, i *interpreter, st *frame, n *node, baseDelay time.Duration) (TaskResult, error, error) {
	t := n.taskPayload()
	action, ok := n.action().(plan.AskModel)
	if !ok {
		return TaskResult{}, nil, invalidActionError(n, "model prompt")
	}
	rt := runtime.Name(resolveRoutingValue(string(action.Runtime), i.opts.Params))
	model := runtime.Model(resolveRoutingValue(string(action.Model), i.opts.Params))
	effort := runtime.Effort(resolveRoutingValue(string(action.Effort), i.opts.Params))
	runner, ok := resolveRunner(i.opts, rt)
	if !ok {
		return TaskResult{}, nil, fmt.Errorf("task %q: runtime %q: %w", t.ID, rt, runtime.ErrUnknownRuntime)
	}
	sysPrompt := action.SystemPrompt.Render(workflow.RenderContext{
		Params: i.opts.Params,
		State:  i.opts.State,
	})
	if i.hooks.OnStart != nil {
		i.hooks.OnStart(*t, st.iteration, rt, model, effort)
	}
	st.mu.Lock()
	body := renderTemplate(action.Prompt, st, i.opts)
	st.mu.Unlock()
	policy := n.policy()
	send := func() (TaskResult, error) {
		return runWithRetry(ctx, t.ID, policy.Retry, policy.Budget, baseDelay, func() (TaskResult, error) {
			r, err := runLLM(ctx, t, body, runner, model, effort, sysPrompt, st.workDir)
			if err != nil {
				return r, err
			}
			return r, evaluateSchemaGate(ctx, t, action.Schema, r.Output, r.ExitCode)
		})
	}
	var (
		res    TaskResult
		runErr error
	)
	if i.opts.Cache != nil && cacheEnabled(i.program.env, n) {
		res, runErr = runCached(i.opts.Cache, t, rt, model, effort, sysPrompt, body, send)
	} else {
		res, runErr = send()
	}
	return res, runErr, nil
}

func (invalidOp) eval(_ context.Context, _ *interpreter, _ *frame, n *node, _ time.Duration) (TaskResult, error, error) {
	return TaskResult{}, nil, fmt.Errorf("task %q: invalid body: exactly one of prompt, command, workflow, or script must be set", n.id())
}
