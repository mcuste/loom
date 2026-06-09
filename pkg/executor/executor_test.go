package executor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

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

// noRunSpec is a RuntimeSpec that does not implement Runtime — exercises the
// executor's type-assertion error path.
type noRunSpec struct{}

func (noRunSpec) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func init() {
	runtime.Register("exec-echo", echoRuntime{})
	runtime.Register("exec-err", errRuntime{})
	runtime.Register("exec-no-run", noRunSpec{})
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

// TestRunSpecWithoutRunRejected pins that registering only the validation
// surface is not enough — execution requires the full Runtime contract.
func TestRunSpecWithoutRunRejected(t *testing.T) {
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: "exec-no-run",
		Model:   "m1",
		Tasks:   []workflow.Task{{ID: "a", Prompt: "x"}},
	}
	_, err := executor.Run(context.Background(), wf, executor.Hooks{})
	if err == nil || !contains(err.Error(), "does not implement Run") {
		t.Fatalf("Run err = %v, want 'does not implement Run'", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
