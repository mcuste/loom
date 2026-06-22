package workflow_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// taskLoopForm declares a loop via a task carrying a `loop:` block: the wrapper
// task id (work) is the loop id and its nested tasks are the body.
const taskLoopForm = `
name: wf_task_loop
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: work
    description: drains the queue
    loop:
      until_empty: drain
      max: 5
      tasks:
        - id: drain
          depends_on: [seed]
          prompt: |
            drain {{seed}} {{prev.refine}}
        - id: refine
          depends_on: [drain]
          prompt: refine {{drain}}
`

// TestParse_TaskLoop_EquivalentToTopLevel pins that the per-task `loop:` form
// desugars into the same LoopGroup and flattened task set as the top-level
// `loops:` block: same loop id, members, convergence target, and cap.
func TestParse_TaskLoop_EquivalentToTopLevel(t *testing.T) {
	got, err := workflow.Parse([]byte(taskLoopForm))
	if err != nil {
		t.Fatalf("Parse(task loop) returned error: %v", err)
	}

	if len(got.Loops) != 1 {
		t.Fatalf("len(Loops) = %d, want 1", len(got.Loops))
	}
	lg := got.Loops[0]
	if lg.ID != "work" {
		t.Errorf("loop id = %q, want %q", lg.ID, "work")
	}
	if lg.UntilEmpty != "drain" {
		t.Errorf("loop until_empty = %q, want %q", lg.UntilEmpty, "drain")
	}
	if lg.Max != 5 {
		t.Errorf("loop max = %d, want 5", lg.Max)
	}
	if want := []workflow.TaskID{"drain", "refine"}; !slices.Equal(lg.Members, want) {
		t.Errorf("loop members = %v, want %v", lg.Members, want)
	}

	// wf.Tasks is the flat union; the wrapper (work) is not itself a task, so the
	// flattened ids are the top-level seed plus the two body members.
	ids := make([]workflow.TaskID, len(got.Tasks))
	for i, task := range got.Tasks {
		ids[i] = task.ID
	}
	if want := []workflow.TaskID{"seed", "drain", "refine"}; !slices.Equal(ids, want) {
		t.Errorf("wf.Tasks ids = %v, want %v", ids, want)
	}
}

// TestParse_TaskLoop_DescriptionFromWrapper pins that the wrapper task's
// description becomes the loop's description (the per-task form has no
// description key inside the block).
func TestParse_TaskLoop_DescriptionFromWrapper(t *testing.T) {
	wf, err := workflow.Parse([]byte(taskLoopForm))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got := wf.Loops[0].Description; got != "drains the queue" {
		t.Errorf("loop description = %q, want %q", got, "drains the queue")
	}
}

// TestParse_TaskLoop_WrapperIsNotATask pins that the loop wrapper never becomes
// an executable task: ByID(work) is nil, and each body member is attributed to
// the loop via Task.Loop.
func TestParse_TaskLoop_WrapperIsNotATask(t *testing.T) {
	wf, err := workflow.Parse([]byte(taskLoopForm))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.ByID("work") != nil {
		t.Errorf("ByID(work) = non-nil, want nil (wrapper is a loop, not a task)")
	}
	for _, id := range []workflow.TaskID{"drain", "refine"} {
		if got := wf.ByID(id); got == nil {
			t.Fatalf("ByID(%q) = nil, want member task", id)
		} else if got.Loop != "work" {
			t.Errorf("task %q Loop = %q, want %q", id, got.Loop, "work")
		}
	}
}

// TestParse_TaskLoop_Rejections pins the loop-wrapper discriminator: a `loop:`
// task may not also set a body, runtime knobs, or task-only fields.
func TestParse_TaskLoop_Rejections(t *testing.T) {
	cases := []struct {
		name    string
		extra   string
		wantErr error
	}{
		{
			name:    "with command",
			extra:   "    command: echo hi\n",
			wantErr: workflow.ErrLoopTaskWithBody,
		},
		{
			name:    "with prompt",
			extra:   "    prompt: do it\n",
			wantErr: workflow.ErrLoopTaskWithBody,
		},
		{
			name:    "with model",
			extra:   "    model: m1\n",
			wantErr: workflow.ErrLoopTaskWithRuntime,
		},
		{
			name:    "with depends_on",
			extra:   "    depends_on: [seed]\n",
			wantErr: workflow.ErrLoopTaskWithFields,
		},
		{
			name:    "with when",
			extra:   "    when: succeeded(seed)\n",
			wantErr: workflow.ErrLoopTaskWithFields,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "name: wf\nruntime: test-rt\nmodel: m1\ntasks:\n" +
				"  - id: seed\n    prompt: seed\n" +
				"  - id: work\n" + tc.extra +
				"    loop:\n      until_empty: drain\n      max: 3\n      tasks:\n" +
				"        - id: drain\n          depends_on: [seed]\n          prompt: drain {{seed}}\n"
			_, err := workflow.Parse([]byte(src))
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Parse error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestParse_TaskLoop_RejectsIDInsideBlock pins that the per-task form does not
// accept an `id:` (or `description:`) key inside the loop block: both come from
// the wrapper task, so they are unknown fields here.
func TestParse_TaskLoop_RejectsIDInsideBlock(t *testing.T) {
	src := "name: wf\nruntime: test-rt\nmodel: m1\ntasks:\n" +
		"  - id: seed\n    prompt: seed\n" +
		"  - id: work\n    loop:\n      id: nope\n      until_empty: drain\n      max: 3\n      tasks:\n" +
		"        - id: drain\n          depends_on: [seed]\n          prompt: drain {{seed}}\n"
	var unknown *workflow.UnknownLoopGroupFieldError
	if _, err := workflow.Parse([]byte(src)); !errors.As(err, &unknown) {
		t.Errorf("Parse error = %v, want UnknownLoopGroupFieldError", err)
	}
}
