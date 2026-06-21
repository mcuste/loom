package executor_test

import (
	"context"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/workflow"
)

// findResult returns the TaskResult for id from a report, or fails the test.
func findResult(t *testing.T, rep *executor.Report, id workflow.TaskID) executor.TaskResult {
	t.Helper()
	for _, r := range rep.Tasks {
		if r.TaskID == id {
			return r
		}
	}
	t.Fatalf("no TaskResult for %q in report", id)
	return executor.TaskResult{}
}

// whenTrueSrc / whenFalseSrc share a three-task shape: a `gate` shell task whose
// output drives `guarded`'s `when:`, and an `after` task that depends on
// `guarded` and substitutes its output. Only the gate's echoed value differs,
// flipping the condition.
const whenTrueSrc = `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: gate
    command: "echo yes"
  - id: guarded
    depends_on: [gate]
    when: '{{gate}} == "yes"'
    prompt: "ran"
  - id: after
    depends_on: [guarded]
    prompt: "after:{{guarded}}"
`

const whenFalseSrc = `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: gate
    command: "echo no"
  - id: guarded
    depends_on: [gate]
    when: '{{gate}} == "yes"'
    prompt: "ran"
  - id: after
    depends_on: [guarded]
    prompt: "after:{{guarded}}"
`

// TestRun_WhenTrueRunsTask pins that a task whose `when:` evaluates true runs
// normally and produces its output.
func TestRun_WhenTrueRunsTask(t *testing.T) {
	wf, err := workflow.Parse([]byte(whenTrueSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["guarded"] != "ran" {
		t.Errorf("Outputs[guarded] = %q, want %q", rep.Outputs["guarded"], "ran")
	}
}

// TestRun_WhenFalseSkipsTask pins that a task whose `when:` evaluates false is
// skipped: it produces empty output.
func TestRun_WhenFalseSkipsTask(t *testing.T) {
	wf, err := workflow.Parse([]byte(whenFalseSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["guarded"] != "" {
		t.Errorf("Outputs[guarded] = %q, want empty (skipped)", rep.Outputs["guarded"])
	}
}

// TestRun_WhenFalseMarksStatusSkipped pins that the skipped task's result
// carries StatusSkipped.
func TestRun_WhenFalseMarksStatusSkipped(t *testing.T) {
	wf, err := workflow.Parse([]byte(whenFalseSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := findResult(t, rep, "guarded")
	if res.Status != executor.StatusSkipped {
		t.Errorf("guarded Status = %q, want %q", res.Status, executor.StatusSkipped)
	}
}

// TestRun_WhenFalseClosesGateForDownstream pins skip-propagation: a skipped
// task still closes its gate, so a downstream task runs and sees the skipped
// task's empty output.
func TestRun_WhenFalseClosesGateForDownstream(t *testing.T) {
	wf, err := workflow.Parse([]byte(whenFalseSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["after"] != "after:" {
		t.Errorf("Outputs[after] = %q, want %q (downstream ran despite skip)", rep.Outputs["after"], "after:")
	}
}

// TestRun_WhenSucceededHelperRuns pins the `succeeded(id)` helper end-to-end:
// when the named dependency ran to completion, the guarded task runs.
func TestRun_WhenSucceededHelperRuns(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: build
    command: "echo ok"
  - id: deploy
    depends_on: [build]
    when: succeeded(build)
    prompt: "deployed"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["deploy"] != "deployed" {
		t.Errorf("Outputs[deploy] = %q, want %q (succeeded(build) is true)", rep.Outputs["deploy"], "deployed")
	}
}

// TestRun_WhenContainsHelperRuns pins the `contains(...)` helper end-to-end:
// when the dependency's output contains the substring, the guarded task runs.
func TestRun_WhenContainsHelperRuns(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: gate
    command: "echo 'an error occurred'"
  - id: guarded
    depends_on: [gate]
    when: 'contains({{gate}}, "error")'
    prompt: "ran"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["guarded"] != "ran" {
		t.Errorf("Outputs[guarded] = %q, want %q (contains matched)", rep.Outputs["guarded"], "ran")
	}
}

// cascadeSrc skips `guarded` (gate echoes "no"), then fans two downstream tasks
// off it: `on_success` guarded by succeeded(guarded) and `on_failure` guarded by
// failed(guarded). A skipped task is neither succeeded nor failed, so both must
// be skipped — `on_failure` must NOT run.
const cascadeSrc = `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: gate
    command: "echo no"
  - id: guarded
    depends_on: [gate]
    when: '{{gate}} == "yes"'
    prompt: "ran"
  - id: on_success
    depends_on: [guarded]
    when: succeeded(guarded)
    prompt: "success"
  - id: on_failure
    depends_on: [guarded]
    when: failed(guarded)
    prompt: "failure"
`

// TestRun_WhenSucceededOfSkippedSkips pins that succeeded(id) of a skipped
// upstream is false, so its success-branch is skipped (cascade propagation).
func TestRun_WhenSucceededOfSkippedSkips(t *testing.T) {
	wf, err := workflow.Parse([]byte(cascadeSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res := findResult(t, rep, "on_success"); res.Status != executor.StatusSkipped {
		t.Errorf("on_success Status = %q, want %q (succeeded of skipped is false)", res.Status, executor.StatusSkipped)
	}
}

// TestRun_WhenFailedOfSkippedSkips is the regression for the skip/fail
// conflation: a task skipped by its own `when:` must NOT be reported as failed,
// so failed(guarded) is false and on_failure is skipped rather than run.
func TestRun_WhenFailedOfSkippedSkips(t *testing.T) {
	wf, err := workflow.Parse([]byte(cascadeSrc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := findResult(t, rep, "on_failure")
	if res.Status != executor.StatusSkipped {
		t.Errorf("on_failure Status = %q, want %q (skipped is not failed)", res.Status, executor.StatusSkipped)
	}
	if rep.Outputs["on_failure"] != "" {
		t.Errorf("Outputs[on_failure] = %q, want empty (must not run)", rep.Outputs["on_failure"])
	}
}

// TestRun_WhenFailedDepAbortsRun documents the current executor semantics that
// make failed(id) a no-op against a *runtime* failure: when a dependency exits
// non-zero, the run aborts (Run returns an error) and the failed()-guarded task
// never reaches its when: evaluation, so it produces no output. This is the
// regression anchor for that behavior; a future continue-on-error executor
// would flip it (the guarded task would run) and must update this test.
func TestRun_WhenFailedDepAbortsRun(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: build
    command: "exit 1"
  - id: on_failure
    depends_on: [build]
    when: failed(build)
    prompt: "recover"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatal("Run returned nil error, want failure from build's non-zero exit")
	}
	if rep.Outputs["on_failure"] != "" {
		t.Errorf("Outputs[on_failure] = %q, want empty (failed dep aborts the run before the guard runs)", rep.Outputs["on_failure"])
	}
}
