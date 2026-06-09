package executor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// echoRuntime is a registered fake runtime that returns Prompt verbatim as the
// output plus deterministic usage. Tests use it to assert that the executor
// substitutes placeholders before calling Run.
type echoRuntime struct{}

func (echoRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	if req.Model != "m1" {
		return fmt.Errorf("model %q: %w", req.Model, runtime.ErrUnsupportedModel)
	}
	return nil
}

func (echoRuntime) Run(ctx context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{InputTokens: 10, OutputTokens: 20, TotalCostUSD: 0.001},
	}, nil
}

// errRuntime always fails Run; used to assert error propagation.
type errRuntime struct{}

func (errRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (errRuntime) Run(ctx context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{}, errors.New("kaboom")
}

// barrierRuntime blocks every Run until the test releases it, letting a test
// prove that independent tasks have all entered Run simultaneously.
type barrierRuntime struct {
	entered chan struct{}
	release chan struct{}
}

func (barrierRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (b barrierRuntime) Run(ctx context.Context, req runtime.Request) (runtime.Response, error) {
	select {
	case b.entered <- struct{}{}:
	case <-ctx.Done():
		return runtime.Response{}, ctx.Err()
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return runtime.Response{}, ctx.Err()
	}
	return runtime.Response{Output: req.Prompt}, nil
}

var execBarrier = barrierRuntime{
	entered: make(chan struct{}, 16),
	release: make(chan struct{}),
}

func init() {
	runtime.Register("exec-echo", echoRuntime{})
	runtime.Register("exec-err", errRuntime{})
	runtime.Register("exec-barrier", execBarrier)
}

// TestRunSubstitutesUpstreamOutputs exercises the end-to-end happy path: the
// executor must walk the DAG in topological order and substitute `{{id}}`
// placeholders with prior task outputs before dispatching each Run.
func TestRunSubstitutesUpstreamOutputs(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: a
    prompt: hello
  - id: b
    depends_on: [a]
    prompt: |
      saw: {{a}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Tasks) != 2 {
		t.Fatalf("Tasks = %d, want 2", len(rep.Tasks))
	}
	if rep.Outputs["a"] != "hello" {
		t.Errorf("Outputs[a] = %q, want hello", rep.Outputs["a"])
	}
	if rep.Outputs["b"] != "saw: hello\n" {
		t.Errorf("Outputs[b] = %q, want %q", rep.Outputs["b"], "saw: hello\n")
	}
	if rep.Usage.InputTokens != 20 || rep.Usage.OutputTokens != 40 {
		t.Errorf("Usage = %+v, want 20 in / 40 out", rep.Usage)
	}
	if rep.Usage.TotalCostUSD != 0.002 {
		t.Errorf("Usage.TotalCostUSD = %v, want 0.002", rep.Usage.TotalCostUSD)
	}
}

// TestRunHooksFireInOrder verifies OnStart fires before OnFinish for each
// task, and that the hook sequence matches Plan order.
func TestRunHooksFireInOrder(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: a
    prompt: x
  - id: b
    depends_on: [a]
    prompt: |
      {{a}}
`
	wf, _ := workflow.Parse([]byte(src))

	var events []string
	hooks := executor.Hooks{
		OnStart: func(t workflow.Task, _ runtime.Name, _ runtime.Model, _ runtime.Effort) {
			events = append(events, "start:"+string(t.ID))
		},
		OnFinish: func(t workflow.Task, _ executor.TaskResult, err error) {
			events = append(events, "finish:"+string(t.ID))
		},
	}
	if _, err := executor.Run(context.Background(), wf, hooks); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"start:a", "finish:a", "start:b", "finish:b"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

// TestRunStopsOnFailure pins that the executor aborts on the first task error
// and returns a partial report containing only the tasks that completed.
func TestRunStopsOnFailure(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: a
    prompt: x
  - id: b
    runtime: exec-err
    depends_on: [a]
    prompt: y
  - id: c
    depends_on: [b]
    prompt: z
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{})
	if err == nil {
		t.Fatalf("Run returned nil error, want failure")
	}
	if rep == nil || len(rep.Tasks) != 1 || rep.Tasks[0].TaskID != "a" {
		t.Fatalf("rep.Tasks = %+v, want exactly [a]", rep)
	}
}

// TestRunIndependentTasksAreConcurrent pins the parallelism contract: three
// tasks with no dependencies between them must all enter Run before any is
// allowed to return. If the executor were serial, only one task would reach
// the barrier and the test would time out.
func TestRunIndependentTasksAreConcurrent(t *testing.T) {
	src := `
name: wf
runtime: exec-barrier
model: m1
tasks:
  - id: a
    prompt: x
  - id: b
    prompt: x
  - id: c
    prompt: x
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := executor.Run(context.Background(), wf, executor.Hooks{})
		done <- err
	}()

	for i := range 3 {
		select {
		case <-execBarrier.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/3 tasks reached Run before timeout — executor is serial", i)
		}
	}
	close(execBarrier.release)

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRunUnknownRuntime exercises the executor's lookup error path.
func TestRunUnknownRuntime(t *testing.T) {
	// Build a Workflow manually (Parse would reject it because the runtime is
	// unknown) to exercise executor.Run's lookup-failure branch.
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: "definitely-not-registered",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "x"},
		},
	}
	_, err := executor.Run(context.Background(), wf, executor.Hooks{})
	if !errors.Is(err, runtime.ErrUnknownRuntime) {
		t.Fatalf("errors.Is ErrUnknownRuntime = false; err = %v", err)
	}
}

