package executor

import (
	"context"

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
	t := i.program.wf.ByID(u.id)
	return runTask(ctx, i.program.wf, t, st, i.hooks, i.opts)
}

func (u loopUnit) run(ctx context.Context, i *interpreter, st *frame) error {
	lg := &i.program.wf.Loops[u.index]
	return runLoop(ctx, i.program.wf, lg, st, i.hooks, i.opts)
}
