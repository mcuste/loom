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
}

func newInterpreter(p *program, hooks Hooks, opts Options) *interpreter {
	return &interpreter{
		program: p,
		hooks:   hooks,
		opts:    opts,
	}
}

func (i *interpreter) run(ctx context.Context, st *frame) error {
	g, gctx := errgroup.WithContext(ctx)
	for _, scheduled := range i.program.units {
		u := scheduled
		g.Go(func() error {
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
	if n == nil || n.task == nil {
		return fmt.Errorf("task %q: compiled node missing", u.id)
	}
	return i.evalNode(ctx, st, n)
}

func (u loopUnit) run(ctx context.Context, i *interpreter, st *frame) error {
	return runLoop(ctx, i, &i.program.wf.Loops[u.index], st)
}

func (i *interpreter) retryBaseDelay() time.Duration {
	baseDelay := i.opts.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = defaultRetryBaseDelay
	}
	return baseDelay
}

func (i *interpreter) evalNode(ctx context.Context, st *frame, n *node) error {
	if n == nil || n.task == nil {
		return fmt.Errorf("compiled node missing")
	}
	t := n.task
	baseDelay := i.retryBaseDelay()

	if err := st.waitDeps(ctx, n.deps); err != nil {
		return err
	}

	if t.Cond != nil {
		run, err := st.evalWhen(t)
		if err != nil {
			return fmt.Errorf("task %q: when: %w", t.ID, err)
		}
		if !run {
			st.recordSkip(t, i.hooks)
			return nil
		}
	}

	if i.program.wf.Budget != nil {
		release, err := st.admitBudget(ctx, i.program.wf)
		if err != nil {
			return err
		}
		defer release()
	}

	res, runErr, fatal := n.op.eval(ctx, i, st, n, baseDelay)
	if fatal != nil {
		return fatal
	}

	res.Iteration = st.iteration
	if i.hooks.OnFinish != nil {
		i.hooks.OnFinish(*t, st.iteration, res, runErr)
	}
	if runErr != nil {
		return fmt.Errorf("task %q: %w", t.ID, runErr)
	}

	st.recordResult(t, res)
	return nil
}
