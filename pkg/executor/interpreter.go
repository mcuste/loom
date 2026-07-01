package executor

import (
	"context"
	"fmt"

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
	return runTask(ctx, i.program.wf, n.task, st, i.hooks, i.opts)
}

func (u loopUnit) run(ctx context.Context, i *interpreter, st *frame) error {
	lg := &i.program.wf.Loops[u.index]
	return runLoop(ctx, i.program.wf, lg, st, i.hooks, i.opts)
}

func (legacyOp) eval(ctx context.Context, i *interpreter, st *frame, n *node) (TaskResult, error, error) {
	baseDelay := i.opts.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = defaultRetryBaseDelay
	}
	return dispatch(ctx, i.program.wf, n.task, st, i.hooks, i.opts, baseDelay)
}
