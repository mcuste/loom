package executor_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// iterRecorder captures the iteration index handed to OnStart and OnFinish,
// keyed by task id, in call order. Hook callbacks fire concurrently across
// loop-body members, so a mutex guards the maps.
type iterRecorder struct {
	mu       sync.Mutex
	onStart  map[workflow.TaskID][]int
	onFinish map[workflow.TaskID][]int
}

func newIterRecorder() *iterRecorder {
	return &iterRecorder{
		onStart:  map[workflow.TaskID][]int{},
		onFinish: map[workflow.TaskID][]int{},
	}
}

func (r *iterRecorder) hooks() executor.Hooks {
	return executor.Hooks{
		OnStart: func(t workflow.Task, iter int, _ runtime.Name, _ runtime.Model, _ runtime.Effort) {
			r.mu.Lock()
			r.onStart[t.ID] = append(r.onStart[t.ID], iter)
			r.mu.Unlock()
		},
		OnFinish: func(t workflow.Task, iter int, _ executor.TaskResult, _ error) {
			r.mu.Lock()
			r.onFinish[t.ID] = append(r.onFinish[t.ID], iter)
			r.mu.Unlock()
		},
	}
}

func (r *iterRecorder) startsFor(id workflow.TaskID) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.onStart[id])
}

func (r *iterRecorder) finishesFor(id workflow.TaskID) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.onFinish[id])
}

// TestRun_ScopedLoop_HooksReceiveIterationIndex pins the iteration index handed
// to both OnStart and OnFinish. A looped body member (c) is stamped with its
// 1-based pass number, so a two-iteration loop records [1, 2]; a non-looped task
// (seed) fires once stamped 0, the documented invariant on plain tasks. The two
// hooks share identical setup, so a table over the per-hook accessor exercises
// both without duplicating the harness.
func TestRun_ScopedLoop_HooksReceiveIterationIndex(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  func(*iterRecorder, workflow.TaskID) []int
	}{
		{"OnStart", (*iterRecorder).startsFor},
		{"OnFinish", (*iterRecorder).finishesFor},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt, _ := newLoopRT(t, convergeScripts(), runtime.Usage{})

			wf, err := workflow.Parse([]byte(convergeWorkflow(rt)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			rec := newIterRecorder()
			if _, err := executor.Run(context.Background(), wf, rec.hooks(), executor.Options{}); err != nil {
				t.Fatalf("Run: %v", err)
			}

			if got := tc.got(rec, "c"); !slices.Equal(got, []int{1, 2}) {
				t.Errorf("%s iterations for looped c = %v, want [1 2]", tc.name, got)
			}
			if got := tc.got(rec, "seed"); !slices.Equal(got, []int{0}) {
				t.Errorf("%s iterations for non-looped seed = %v, want [0]", tc.name, got)
			}
		})
	}
}
