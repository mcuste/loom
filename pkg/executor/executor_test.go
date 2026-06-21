package executor_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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

// systemPromptCaptureRuntime records the SystemPrompt from the most recent
// Run call. Used by TestRunSystemPromptParamSubstitution to assert that the
// executor performs param substitution before constructing the Request.
type systemPromptCaptureRuntime struct {
	mu       sync.Mutex
	captured string
}

func (r *systemPromptCaptureRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (r *systemPromptCaptureRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	r.mu.Lock()
	r.captured = req.SystemPrompt
	r.mu.Unlock()
	return runtime.Response{Output: req.Prompt}, nil
}

var execBarrier = barrierRuntime{
	entered: make(chan struct{}, 16),
	release: make(chan struct{}),
}

var execSysCapture = &systemPromptCaptureRuntime{}

func init() {
	runtime.Register("exec-echo", echoRuntime{})
	runtime.Register("exec-err", errRuntime{})
	runtime.Register("exec-barrier", execBarrier)
	runtime.Register("exec-syscapture", execSysCapture)
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
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
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
		OnStart: func(t workflow.Task, _ int, _ runtime.Name, _ runtime.Model, _ runtime.Effort) {
			events = append(events, "start:"+string(t.ID))
		},
		OnFinish: func(t workflow.Task, _ int, _ executor.TaskResult, err error) {
			events = append(events, "finish:"+string(t.ID))
		},
	}
	if _, err := executor.Run(context.Background(), wf, hooks, executor.Options{}); err != nil {
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
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
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
		_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
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
	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if !errors.Is(err, runtime.ErrUnknownRuntime) {
		t.Fatalf("errors.Is ErrUnknownRuntime = false; err = %v", err)
	}
}

// TestRunSubstitutesParams verifies that {{params.name}} placeholders in task
// prompts are substituted with opts.Params values before the runtime sees them.
func TestRunSubstitutesParams(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
params:
  - name: env
    default: dev
tasks:
  - id: a
    prompt: "target={{params.env}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts := executor.Options{Params: workflow.ParamValues{"env": "prod"}}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["a"] != "target=prod" {
		t.Errorf("Outputs[a] = %q, want %q", rep.Outputs["a"], "target=prod")
	}
}

// TestRunSystemPromptParamSubstitution verifies that the executor substitutes
// {{params.name}} in the workflow-level system_prompt before constructing the
// runtime.Request, so the runtime sees the final resolved text.
func TestRunSystemPromptParamSubstitution(t *testing.T) {
	// Reset the capture from any prior test.
	execSysCapture.mu.Lock()
	execSysCapture.captured = ""
	execSysCapture.mu.Unlock()

	src := `
name: wf
runtime: exec-syscapture
model: m1
system_prompt: "ctx={{params.env}}"
params:
  - name: env
    default: dev
tasks:
  - id: a
    prompt: "hello"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts := executor.Options{Params: workflow.ParamValues{"env": "staging"}}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	execSysCapture.mu.Lock()
	got := execSysCapture.captured
	execSysCapture.mu.Unlock()

	if got != "ctx=staging" {
		t.Errorf("SystemPrompt = %q, want %q", got, "ctx=staging")
	}
}

// TestRunParamsImmutableUnderRace verifies that three independent sibling tasks
// all reading opts.Params concurrently introduce no data race. Run under
// `go test -race` to assert the absence of concurrent map read/write.
func TestRunParamsImmutableUnderRace(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
params:
  - name: x
    default: original
tasks:
  - id: a
    prompt: "{{params.x}}"
  - id: b
    prompt: "{{params.x}}"
  - id: c
    prompt: "{{params.x}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts := executor.Options{Params: workflow.ParamValues{"x": "racetest"}}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, res := range rep.Tasks {
		if res.Prompt != "racetest" {
			t.Errorf("task %q Prompt = %q, want %q", res.TaskID, res.Prompt, "racetest")
		}
	}
}

// TestShellHappyPath verifies that a shell task with `command: echo hi`
// produces "hi" as output and zero Usage.
func TestShellHappyPath(t *testing.T) {
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "a", Command: "echo hi"},
		},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["a"]; got != "hi" {
		t.Errorf("Outputs[a] = %q, want %q", got, "hi")
	}
	if len(rep.Tasks) != 1 {
		t.Fatalf("Tasks = %d, want 1", len(rep.Tasks))
	}
	if (rep.Tasks[0].Usage != runtime.Usage{}) {
		t.Errorf("Usage = %+v, want zero", rep.Tasks[0].Usage)
	}
}

// TestShellFailure verifies that a command exiting non-zero returns a
// *executor.ShellError carrying the right ExitCode and non-empty Stderr.
func TestShellFailure(t *testing.T) {
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "a", Command: "echo 'something went wrong' >&2; exit 2"},
		},
	}
	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatal("Run returned nil error, want failure")
	}
	var shellErr *executor.ShellError
	if !errors.As(err, &shellErr) {
		t.Fatalf("error is %T, want *executor.ShellError; err = %v", err, err)
	}
	if shellErr.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", shellErr.ExitCode)
	}
	if strings.TrimSpace(shellErr.Stderr) == "" {
		t.Errorf("Stderr is empty, want non-empty")
	}
}

// TestShellContextCancel verifies that cancelling the context interrupts a
// long-running shell command and returns an error within a short deadline.
func TestShellContextCancel(t *testing.T) {
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "a", Command: "sleep 60"},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, wf, executor.Hooks{}, executor.Options{})
		done <- err
	}()

	// Give the process a moment to start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil error after context cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after context cancel")
	}
}

// TestShellFeedsLLMDownstream verifies a mixed DAG: a shell task's stdout
// becomes a placeholder substituted into a downstream LLM task's prompt.
func TestShellFeedsLLMDownstream(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: shell_out
    command: "echo greetings"
  - id: llm_in
    depends_on: [shell_out]
    prompt: "got: {{shell_out}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["shell_out"]; got != "greetings" {
		t.Errorf("Outputs[shell_out] = %q, want %q", got, "greetings")
	}
	if got := rep.Outputs["llm_in"]; got != "got: greetings" {
		t.Errorf("Outputs[llm_in] = %q, want %q", got, "got: greetings")
	}
}

// TestRunReportCarriesParams asserts that rep.Params reflects opts.Params
// verbatim so callers can read what substituted without re-resolving.
func TestRunReportCarriesParams(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
params:
  - name: env
    default: dev
tasks:
  - id: a
    prompt: "{{params.env}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := workflow.ParamValues{"env": "prod"}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{Params: want})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Params["env"] != "prod" {
		t.Errorf("rep.Params[env] = %q, want %q", rep.Params["env"], "prod")
	}
	if len(rep.Params) != len(want) {
		t.Errorf("rep.Params len = %d, want %d", len(rep.Params), len(want))
	}
}
