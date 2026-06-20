package executor_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// flakySeq makes each newFlaky registration name unique within a test binary
// run. The runtime registry has no deregister and panics on a duplicate name,
// so a per-test name alone would collide under `go test -count=N`; the atomic
// suffix keeps every registration distinct across counts and parallel tests.
var flakySeq atomic.Uint64

// flakyRuntime fails its first failUpTo Run calls with failErr, then succeeds
// by echoing the prompt. It counts calls so tests can assert how many attempts
// the executor made. A fresh value is constructed per test, so its state is
// never shared across parallel tests.
type flakyRuntime struct {
	mu       sync.Mutex
	calls    int
	failUpTo int
	failErr  error
}

func (f *flakyRuntime) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *flakyRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (f *flakyRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	failUpTo := f.failUpTo
	err := f.failErr
	f.mu.Unlock()
	if n <= failUpTo {
		return runtime.Response{}, err
	}
	return runtime.Response{Output: req.Prompt}, nil
}

// newFlaky registers a fresh flakyRuntime under a name unique to t and returns
// the runtime name (for use as the workflow's `runtime:`) and the stub. The
// per-test name lets independent tests run with t.Parallel() without sharing
// runtime state.
func newFlaky(t *testing.T, failUpTo int, err error) (string, *flakyRuntime) {
	t.Helper()
	f := &flakyRuntime{failUpTo: failUpTo, failErr: err}
	name := "retry-flaky-" + t.Name() + "-" + strconv.FormatUint(flakySeq.Add(1), 10)
	runtime.Register(runtime.Name(name), f)
	return name, f
}

const transientMsg = "rate limit exceeded"

// fastBackoff is a tiny base delay passed via Options so retries do not sleep
// real seconds; carried per-call, it is safe under t.Parallel().
const fastBackoff = time.Millisecond

// TestRun_RetriesTransientErrorThenSucceeds: a task whose runtime fails twice
// with a transient error and then succeeds completes when retry.max allows it.
func TestRun_RetriesTransientErrorThenSucceeds(t *testing.T) {
	t.Parallel()
	rt, flaky := newFlaky(t, 2, errors.New(transientMsg))

	src := `
name: wf
runtime: ` + rt + `
model: m1
tasks:
  - id: a
    prompt: hello
    retry:
      max: 3
      backoff: none
      on: [transient]
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{RetryBaseDelay: fastBackoff})
	if err != nil {
		t.Fatalf("Run: unexpected error after retries: %v", err)
	}
	if rep.Outputs["a"] != "hello" {
		t.Errorf("Outputs[a] = %q, want hello", rep.Outputs["a"])
	}
	if got := flaky.callCount(); got != 3 {
		t.Errorf("attempts = %d, want 3 (1 initial + 2 retries)", got)
	}
}

// TestRun_StopsAfterMaxRetries: a runtime that always fails transiently is
// attempted exactly max+1 times, then Run returns the error.
func TestRun_StopsAfterMaxRetries(t *testing.T) {
	t.Parallel()
	rt, flaky := newFlaky(t, 99, errors.New(transientMsg))

	src := `
name: wf
runtime: ` + rt + `
model: m1
tasks:
  - id: a
    prompt: hello
    retry:
      max: 2
      backoff: none
      on: [transient]
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{RetryBaseDelay: fastBackoff}); err == nil {
		t.Fatalf("Run: expected error after exhausting retries, got nil")
	}
	if got := flaky.callCount(); got != 3 {
		t.Errorf("attempts = %d, want 3 (1 initial + 2 retries)", got)
	}
}

// TestRun_DoesNotRetryNonTransientError: a non-transient failure is attempted
// once even when retry is enabled.
func TestRun_DoesNotRetryNonTransientError(t *testing.T) {
	t.Parallel()
	rt, flaky := newFlaky(t, 99, errors.New("400 invalid request"))

	src := `
name: wf
runtime: ` + rt + `
model: m1
tasks:
  - id: a
    prompt: hello
    retry:
      max: 3
      backoff: none
      on: [transient]
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{RetryBaseDelay: fastBackoff}); err == nil {
		t.Fatalf("Run: expected error, got nil")
	}
	if got := flaky.callCount(); got != 1 {
		t.Errorf("attempts = %d, want 1 (non-transient is not retried)", got)
	}
}

// TestRun_NoRetryBlockDoesNotRetry: a task without a retry block fails on the
// first transient error, preserving today's behavior.
func TestRun_NoRetryBlockDoesNotRetry(t *testing.T) {
	t.Parallel()
	rt, flaky := newFlaky(t, 99, errors.New(transientMsg))

	src := `
name: wf
runtime: ` + rt + `
model: m1
tasks:
  - id: a
    prompt: hello
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{RetryBaseDelay: fastBackoff}); err == nil {
		t.Fatalf("Run: expected error, got nil")
	}
	if got := flaky.callCount(); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry block)", got)
	}
}

// TestRun_BackoffRespectsContextCancellation: cancelling ctx during the backoff
// sleep aborts the wait promptly instead of sleeping the full base delay,
// surfaces context.Canceled, and makes no further attempt.
func TestRun_BackoffRespectsContextCancellation(t *testing.T) {
	t.Parallel()
	// Large base delay: if cancellation is not honored, Run would block ~30s.
	rt, flaky := newFlaky(t, 99, errors.New(transientMsg))

	src := `
name: wf
runtime: ` + rt + `
model: m1
tasks:
  - id: a
    prompt: hello
    retry:
      max: 3
      backoff: exponential
      on: [transient]
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()

	start := time.Now()
	_, err = executor.Run(ctx, wf, executor.Hooks{}, executor.Options{RetryBaseDelay: 30 * time.Second})
	if err == nil {
		t.Fatalf("Run: expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %v; backoff did not honor ctx cancellation", elapsed)
	}
	if got := flaky.callCount(); got != 1 {
		t.Errorf("attempts = %d, want 1 (cancelled during first backoff)", got)
	}
}
