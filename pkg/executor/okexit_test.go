package executor_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// exitRuntime is a fake runtime that mimics a binary exiting with a fixed code:
// it returns a runtime.ExecError carrying that code, exactly as Spec.Run does on
// a non-zero exit. Used to drive the ok_exit tolerance path for LLM tasks.
type exitRuntime struct{ code int }

func (exitRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (r exitRuntime) Run(ctx context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{ExitCode: r.code}, &runtime.ExecError{
		Name:     "exit-rt",
		Err:      fmt.Errorf("exit status %d", r.code),
		ExitCode: r.code,
	}
}

// newExitRT registers an exitRuntime under a name unique to t.
func newExitRT(t *testing.T, code int) string {
	t.Helper()
	name := "exec-exit-" + t.Name() + "-" + strconv.FormatUint(barrierSeq.Add(1), 10)
	runtime.Register(runtime.Name(name), exitRuntime{code: code})
	return name
}

// TestCommandOkExitTolerated verifies a command task with ok_exit treats a listed
// non-zero exit as success: the code is captured and stdout flows downstream.
func TestCommandOkExitTolerated(t *testing.T) {
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "check", Command: "echo hi; exit 2", OkExit: []int{2}},
			{ID: "report", Command: "echo got={{check.exit}} out={{check}}", DependsOn: []workflow.TaskID{"check"}},
		},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["report"]; got != "got=2 out=hi" {
		t.Errorf("Outputs[report] = %q, want %q", got, "got=2 out=hi")
	}
	for _, r := range rep.Tasks {
		if r.TaskID == "check" && r.ExitCode != 2 {
			t.Errorf("check ExitCode = %d, want 2", r.ExitCode)
		}
	}
}

// TestCommandNonToleratedExitFails verifies a command exit outside ok_exit still
// fails the task, and the failure result still carries the exit code.
func TestCommandNonToleratedExitFails(t *testing.T) {
	var failCode int
	hooks := executor.Hooks{
		OnFinish: func(_ workflow.Task, _ int, res executor.TaskResult, err error) {
			if err != nil {
				failCode = res.ExitCode
			}
		},
	}
	wf := &workflow.Workflow{
		ID:    "wf",
		Tasks: []workflow.Task{{ID: "a", Command: "exit 3", OkExit: []int{1}}},
	}
	_, err := executor.Run(context.Background(), wf, hooks, executor.Options{})
	if err == nil {
		t.Fatal("Run returned nil error, want failure for untolerated exit 3")
	}
	var shellErr *executor.ShellError
	if !errors.As(err, &shellErr) || shellErr.ExitCode != 3 {
		t.Errorf("error = %v, want *ShellError with ExitCode 3", err)
	}
	if failCode != 3 {
		t.Errorf("OnFinish result ExitCode = %d, want 3 (recorded on failure)", failCode)
	}
}

// TestCommandExitCodeRecordedOnPlainFailure verifies that even without ok_exit, a
// failing command records its numeric exit code on the result (observability).
func TestCommandExitCodeRecordedOnPlainFailure(t *testing.T) {
	var failCode int
	hooks := executor.Hooks{
		OnFinish: func(_ workflow.Task, _ int, res executor.TaskResult, err error) {
			if err != nil {
				failCode = res.ExitCode
			}
		},
	}
	wf := &workflow.Workflow{
		ID:    "wf",
		Tasks: []workflow.Task{{ID: "a", Command: "exit 5"}},
	}
	if _, err := executor.Run(context.Background(), wf, hooks, executor.Options{}); err == nil {
		t.Fatal("Run returned nil error, want failure")
	}
	if failCode != 5 {
		t.Errorf("recorded ExitCode = %d, want 5", failCode)
	}
}

// TestLLMOkExitTolerated verifies an LLM task whose runtime exits with a tolerated
// code succeeds with the code captured for downstream branching.
func TestLLMOkExitTolerated(t *testing.T) {
	rtName := newExitRT(t, 1)
	src := fmt.Sprintf(`
name: wf
runtime: %s
model: m1
tasks:
  - id: gen
    prompt: do it
    ok_exit: [1]
  - id: report
    depends_on: [gen]
    command: "echo code={{gen.exit}}"
`, rtName)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run returned error, want tolerated success: %v", err)
	}
	if got := rep.Outputs["report"]; got != "code=1" {
		t.Errorf("Outputs[report] = %q, want %q", got, "code=1")
	}
}

// TestScriptOkExitNarrows verifies that an ok_exit list on a script task narrows
// the default tolerate-everything behavior: a listed code succeeds, an unlisted
// code fails (unlike a bare script, which tolerates both).
func TestScriptOkExitNarrows(t *testing.T) {
	t.Run("listed code tolerated", func(t *testing.T) {
		path := writeScript(t, "#!/bin/sh\nexit 2\n")
		wf := &workflow.Workflow{
			ID:    "wf",
			Tasks: []workflow.Task{{ID: "a", Script: path, OkExit: []int{2}}},
		}
		rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
		if err != nil {
			t.Fatalf("Run: exit 2 is in ok_exit, want success: %v", err)
		}
		if rep.Tasks[0].ExitCode != 2 {
			t.Errorf("ExitCode = %d, want 2", rep.Tasks[0].ExitCode)
		}
	})
	t.Run("unlisted code fails", func(t *testing.T) {
		path := writeScript(t, "#!/bin/sh\nexit 3\n")
		wf := &workflow.Workflow{
			ID:    "wf",
			Tasks: []workflow.Task{{ID: "a", Script: path, OkExit: []int{2}}},
		}
		if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err == nil {
			t.Fatal("Run returned nil error; exit 3 is outside ok_exit and must fail")
		}
	})
}

// TestLLMOkExitSkipsSchema verifies that an LLM task with both ok_exit and a
// schema does NOT validate its (empty) output when it exits with a tolerated
// non-zero code: the runtime produced no response to validate.
func TestLLMOkExitSkipsSchema(t *testing.T) {
	rtName := newExitRT(t, 1)
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: runtime.Name(rtName),
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "gen", Prompt: "go", OkExit: []int{1}, Schema: objectSchema()},
		},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: a tolerated non-zero exit must skip schema validation, got: %v", err)
	}
	if rep.Tasks[0].ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", rep.Tasks[0].ExitCode)
	}
}

// TestLLMExitFailsWithoutOkExit verifies the default: a runtime non-zero exit
// fails the LLM task and aborts when ok_exit does not tolerate it.
func TestLLMExitFailsWithoutOkExit(t *testing.T) {
	rtName := newExitRT(t, 1)
	src := fmt.Sprintf(`
name: wf
runtime: %s
model: m1
tasks:
  - id: gen
    prompt: do it
`, rtName)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err == nil {
		t.Fatal("Run returned nil error, want failure for untolerated LLM exit")
	}
}
