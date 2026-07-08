package executor

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"
)

type interpreter struct {
	program *program
	hooks   Hooks
	opts    Options
	trace   traceSink
}

func newInterpreter(p *program, hooks Hooks, opts Options) *interpreter {
	return newInterpreterWithTrace(p, hooks, opts, nil)
}

func newInterpreterWithTrace(p *program, hooks Hooks, opts Options, trace traceSink) *interpreter {
	return &interpreter{
		program: p,
		hooks:   hooks,
		opts:    opts,
		trace:   trace,
	}
}

func (i *interpreter) run(ctx context.Context, st *frame) error {
	if i.trace != nil {
		i.trace.ProgramStart(i.program)
	}
	g, gctx := errgroup.WithContext(ctx)
	for _, scheduled := range i.program.units {
		u := scheduled
		g.Go(func() error {
			if i.trace != nil {
				i.trace.UnitStart(u)
			}
			return u.run(gctx, i, st)
		})
	}
	return g.Wait()
}

func (u taskUnit) run(ctx context.Context, i *interpreter, st *frame) error {
	if _, seeded := i.opts.Seed[u.id]; seeded {
		return nil
	}
	n := i.program.nodes[u.id]
	if n == nil || n.task.ID == "" {
		return fmt.Errorf("task %q: compiled node missing", u.id)
	}
	return i.evalNode(ctx, st, n)
}

func (u loopUnit) run(ctx context.Context, i *interpreter, st *frame) error {
	if u.index < 0 || u.index >= len(i.program.loops) {
		return fmt.Errorf("loop %d: compiled loop missing", u.index)
	}
	return i.evalLoop(ctx, st, i.program.loops[u.index])
}

func (i *interpreter) retryBaseDelay() time.Duration {
	baseDelay := i.opts.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = defaultRetryBaseDelay
	}
	return baseDelay
}

func (i *interpreter) evalNode(ctx context.Context, st *frame, n *node) error {
	if n == nil || n.task.ID == "" {
		return fmt.Errorf("compiled node missing")
	}
	t := &n.task
	if i.trace != nil {
		i.trace.NodeStart(n)
	}
	var (
		traceRes TaskResult
		traceErr error
	)
	defer func() {
		if i.trace != nil {
			i.trace.NodeFinish(n, traceRes, traceErr)
		}
	}()
	baseDelay := i.retryBaseDelay()

	if err := st.waitDeps(ctx, n.deps); err != nil {
		traceErr = err
		return err
	}

	release, skipped, err := i.evaluatePreStepGates(ctx, st, n)
	if err != nil {
		traceErr = err
		return err
	}
	if skipped {
		traceRes = TaskResult{TaskID: t.ID, Status: StatusSkipped, Iteration: st.iteration}
		st.recordSkip(t, i.hooks)
		return nil
	}
	defer release()

	res, runErr, fatal := n.op.eval(ctx, i, st, n, baseDelay)
	if fatal != nil {
		traceErr = fatal
		return fatal
	}

	res.Iteration = st.iteration
	if i.hooks.OnFinish != nil {
		i.hooks.OnFinish(*t, st.iteration, res, runErr)
	}
	if runErr != nil {
		traceRes = res
		traceErr = fmt.Errorf("task %q: %w", t.ID, runErr)
		return traceErr
	}

	res.Status = StatusOK
	st.recordResult(t, res)
	traceRes = res
	return nil
}

func (i *interpreter) evalLoop(ctx context.Context, st *frame, lp *loopProgram) error {
	if lp == nil || lp.group.ID == "" {
		return fmt.Errorf("compiled loop missing")
	}
	return i.runLoop(ctx, st, lp)
}
