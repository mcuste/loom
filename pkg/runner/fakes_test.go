package runner

import (
	"context"
	"errors"

	"github.com/mcuste/loom/pkg/runtime"
)

// This file registers the no-binary fake runtimes the pkg/runner tests rely on.
// They mirror the fakes in cmd/loom/fakes_test.go; each lives in the package
// whose test suite needs them so neither pulls in the other's test binary.

// cmdEchoRuntime returns the substituted prompt verbatim so a test can confirm
// param substitution happened.
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

// cmdFailRuntime always errors on Run; seed/resume tests wire a task to it so
// that success proves the executor bypassed it entirely.
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
