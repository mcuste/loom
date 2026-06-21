package executor_test

import (
	"context"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestRunSeed_BypassesRuntimeForSeededTask pins the core resume contract: a
// task whose id is present in Options.Seed has its gate closed with the seeded
// value before any goroutine launches, so the registered runtime is never
// invoked for it. The seeded output flows into downstream `{{id}}`
// substitution just as if the task had run this invocation.
//
// `a` is wired to exec-err (which would error if dispatched); the run still
// succeeds because the seed bypasses the runtime entirely.
func TestRunSeed_BypassesRuntimeForSeededTask(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: a
    runtime: exec-err
    prompt: ignored
  - id: b
    depends_on: [a]
    prompt: |
      saw: {{a}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts := executor.Options{
		Seed: map[workflow.TaskID]string{"a": "from-cache"},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["a"] != "from-cache" {
		t.Errorf("Outputs[a] = %q, want %q", rep.Outputs["a"], "from-cache")
	}
	if rep.Outputs["b"] != "saw: from-cache\n" {
		t.Errorf("Outputs[b] = %q, want %q", rep.Outputs["b"], "saw: from-cache\n")
	}
}

// TestRunSeed_DoesNotFireHooksForSeededTask pins that OnStart and OnFinish
// never fire for a seeded task: the executor closes its gate up-front rather
// than launching a goroutine, so observers see exactly the events for the
// tasks that re-ran.
func TestRunSeed_DoesNotFireHooksForSeededTask(t *testing.T) {
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
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var events []string
	hooks := executor.Hooks{
		OnStart: func(t workflow.Task, _ int, _ runtime.Name, _ runtime.Model, _ runtime.Effort) {
			events = append(events, "start:"+string(t.ID))
		},
		OnFinish: func(t workflow.Task, _ int, _ executor.TaskResult, _ error) {
			events = append(events, "finish:"+string(t.ID))
		},
	}
	opts := executor.Options{Seed: map[workflow.TaskID]string{"a": "preset"}}
	if _, err := executor.Run(context.Background(), wf, hooks, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"start:b", "finish:b"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

// TestRunSeed_DiamondConcurrentDependents pins that a seeded value is visible
// to multiple unseeded downstream tasks running concurrently. With a diamond
// DAG (one seeded root, two dependents), both dependent goroutines race to
// lock rep.Outputs around their Substitute call; if the executor seeded the
// gate but not the output (or vice-versa), one of the two would observe the
// zero value under contention.
func TestRunSeed_DiamondConcurrentDependents(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: a
    runtime: exec-err
    prompt: ignored
  - id: b
    depends_on: [a]
    prompt: |
      left: {{a}}
  - id: c
    depends_on: [a]
    prompt: |
      right: {{a}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts := executor.Options{Seed: map[workflow.TaskID]string{"a": "seed-a"}}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["b"] != "left: seed-a\n" {
		t.Errorf("Outputs[b] = %q, want %q", rep.Outputs["b"], "left: seed-a\n")
	}
	if rep.Outputs["c"] != "right: seed-a\n" {
		t.Errorf("Outputs[c] = %q, want %q", rep.Outputs["c"], "right: seed-a\n")
	}
	if len(rep.Tasks) != 2 {
		t.Fatalf("rep.Tasks = %d entries, want 2 (a is seeded; b and c ran)", len(rep.Tasks))
	}
}

// TestRunSeed_ReportTasksOmitsSeeded pins that rep.Tasks contains only the
// tasks that actually ran this invocation. Seeded tasks are recorded in
// rep.Outputs (for placeholder lookup) but not in rep.Tasks, so the caller
// can tell which subset of the DAG ran on this resume.
func TestRunSeed_ReportTasksOmitsSeeded(t *testing.T) {
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
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts := executor.Options{Seed: map[workflow.TaskID]string{"a": "preset"}}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Tasks) != 1 {
		t.Fatalf("rep.Tasks = %d entries, want 1 (seeded task should be omitted)", len(rep.Tasks))
	}
	if rep.Tasks[0].TaskID != "b" {
		t.Errorf("rep.Tasks[0].TaskID = %q, want b", rep.Tasks[0].TaskID)
	}
}
