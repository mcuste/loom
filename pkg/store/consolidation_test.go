package store_test

import (
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// These tests pin the type-consolidation contract: store.Run.OnFinish now
// accepts an executor.TaskResult directly and reads the fields it persists
// (Prompt, Command, Output, Usage, Elapsed) straight off that value, with no
// intermediate store-owned result type. Each sub-test drives a single OnFinish
// call and asserts one persisted field so a regression names the exact field
// that stopped flowing through. readRun and the deterministic clock/rand
// helpers are shared from store_test.go in this package.

// openRun opens a run rooted at a fresh temp dir with deterministic id inputs.
// Failing fast on the open error keeps each scenario test focused on the one
// field it asserts.
func openRun(t *testing.T) *store.Run {
	t.Helper()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{
		Root: t.TempDir(),
		Now:  fixedClock("2026-06-09T14:30:52Z"),
		Rand: counterRand(1),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return run
}

// TestOnFinishPersistsExecutorResultFields pins, one field per sub-test, that
// each field of an executor.TaskResult lands in the on-disk task record. The
// cases are fully independent, so they run in parallel.
func TestOnFinishPersistsExecutorResultFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		rt     runtime.Name
		model  runtime.Model
		effort runtime.Effort
		result executor.TaskResult
		want   func(t *testing.T, task map[string]any)
	}{
		{
			name: "prompt",
			rt:   "claude-code", model: "sonnet", effort: "medium",
			result: executor.TaskResult{Prompt: "substituted prompt"},
			want: func(t *testing.T, task map[string]any) {
				if task["prompt"] != "substituted prompt" {
					t.Fatalf("tasks[0].prompt = %v, want %q", task["prompt"], "substituted prompt")
				}
			},
		},
		{
			name: "output",
			rt:   "claude-code", model: "sonnet", effort: "medium",
			result: executor.TaskResult{Output: "model output"},
			want: func(t *testing.T, task map[string]any) {
				if task["output"] != "model output" {
					t.Fatalf("tasks[0].output = %v, want %q", task["output"], "model output")
				}
			},
		},
		{
			// Shell tasks carry no runtime/model/effort; executor.TaskResult is the
			// single owner that carries Command.
			name:   "command",
			result: executor.TaskResult{Command: "echo {{msg}}"},
			want: func(t *testing.T, task map[string]any) {
				if task["command"] != "echo {{msg}}" {
					t.Fatalf("tasks[0].command = %v, want %q", task["command"], "echo {{msg}}")
				}
			},
		},
		{
			name: "usage",
			rt:   "claude-code", model: "sonnet", effort: "medium",
			result: executor.TaskResult{Usage: runtime.Usage{InputTokens: 10, OutputTokens: 20, TotalCostUSD: 0.5}},
			want: func(t *testing.T, task map[string]any) {
				usage, ok := task["usage"].(map[string]any)
				if !ok {
					t.Fatalf("tasks[0].usage = %v, want a usage object", task["usage"])
				}
				if v, _ := usage["input_tokens"].(float64); int(v) != 10 {
					t.Fatalf("tasks[0].usage.input_tokens = %v, want 10", usage["input_tokens"])
				}
			},
		},
		{
			name: "elapsed",
			rt:   "claude-code", model: "sonnet", effort: "medium",
			result: executor.TaskResult{Elapsed: 250 * time.Millisecond},
			want: func(t *testing.T, task map[string]any) {
				if v, _ := task["elapsed_ms"].(float64); int64(v) != 250 {
					t.Fatalf("tasks[0].elapsed_ms = %v, want 250", task["elapsed_ms"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			run := openRun(t)
			task := workflow.Task{ID: "alpha"}
			run.OnStart(task, tt.rt, tt.model, tt.effort)
			run.OnFinish(task, tt.result, nil)

			got := readRun(t, run.Path())["tasks"].([]any)[0].(map[string]any)
			tt.want(t, got)
		})
	}
}
