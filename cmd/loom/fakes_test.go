package main

import (
	"context"
	"errors"

	"github.com/mcuste/loom/pkg/runtime"
)

// This file is the single home for the no-binary fake runtimes the cmd/loom
// tests register into the global runtime registry. Keeping them (and the lone
// init that registers them) in one place makes the available fakes discoverable
// and prevents a name collision from hiding in a scattered init.

// cmdEchoRuntime returns the substituted prompt verbatim so a test can confirm
// param substitution happened before the executor dispatched the request.
type cmdEchoRuntime struct{}

func (cmdEchoRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdEchoRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// cmdFailRuntime always fails Run; the seed and resume tests wire a task to it
// so a test can prove the executor bypassed that task entirely. If it were
// dispatched, Run would error and the run would fail.
type cmdFailRuntime struct{}

func (cmdFailRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdFailRuntime) Run(_ context.Context, _ runtime.Request) (runtime.Response, error) {
	return runtime.Response{}, errors.New("cmd-fail must never be dispatched")
}

// cmdCostRuntime succeeds and reports a fixed cost so a chained workflow
// accumulates a predictable TotalCostUSD and trips the workflow budget.
type cmdCostRuntime struct{}

func (cmdCostRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdCostRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{TotalCostUSD: 0.5},
	}, nil
}

func init() {
	runtime.Register("cmd-echo", cmdEchoRuntime{})
	runtime.Register("cmd-fail", cmdFailRuntime{})
	runtime.Register("cmd-cost", cmdCostRuntime{})
}
