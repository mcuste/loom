package workflow_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// validNestedLoop is a workflow with one scoped loop, declared inline as a task
// carrying a `loop:` block. A top-level `seed` task feeds a two-member loop
// {drain, refine}: drain depends on seed and the prior iteration's refine (via
// {{prev.refine}}); refine depends on drain. The loop converges when drain
// drains.
const validNestedLoop = `
name: wf_nested
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: work
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

// TestParse_NestedLoop_FlattensTasksInDeclarationOrder pins that wf.Tasks is the
// flat union of top-level tasks and every loop's nested tasks, in declaration
// order, so existing code over wf.Tasks is unchanged. The loop wrapper (work)
// is not itself a task.
func TestParse_NestedLoop_FlattensTasksInDeclarationOrder(t *testing.T) {
	wf, err := workflow.Parse([]byte(validNestedLoop))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	got := make([]workflow.TaskID, len(wf.Tasks))
	for i, task := range wf.Tasks {
		got[i] = task.ID
	}
	want := []workflow.TaskID{"seed", "drain", "refine"}
	if !slices.Equal(got, want) {
		t.Errorf("wf.Tasks ids = %v, want %v", got, want)
	}
}

// validUntilLoop is a workflow whose scoped loop converges via an `until`
// expression over a member output rather than until_empty.
const validUntilLoop = `
name: wf_until
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: work
    loop:
      until: '{{drain}} == "done"'
      max: 3
      tasks:
        - id: drain
          depends_on: [seed]
          prompt: drain {{seed}}
        - id: refine
          depends_on: [drain]
          prompt: refine {{drain}}
`

// TestParse_NestedLoop_UntilExpression pins the until convergence path: the
// expression compiles bounded to the member set and lands in
// LoopGroup.Until/Cond, with UntilEmpty left empty.
func TestParse_NestedLoop_UntilExpression(t *testing.T) {
	wf, err := workflow.Parse([]byte(validUntilLoop))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(wf.Loops) != 1 {
		t.Fatalf("len(wf.Loops) = %d, want 1", len(wf.Loops))
	}
	lg := wf.Loops[0]
	if lg.Until != `{{drain}} == "done"` {
		t.Errorf("LoopGroup.Until = %q, want %q", lg.Until, `{{drain}} == "done"`)
	}
	if lg.Cond == nil {
		t.Error("LoopGroup.Cond = nil, want compiled condition")
	}
	if lg.UntilEmpty != "" {
		t.Errorf("LoopGroup.UntilEmpty = %q, want empty", lg.UntilEmpty)
	}
}

// TestParse_NestedLoop_PopulatesLoopGroup pins that wf.Loops carries the loop
// id (the wrapper task id), convergence target, max, and the member task ids.
func TestParse_NestedLoop_PopulatesLoopGroup(t *testing.T) {
	wf, err := workflow.Parse([]byte(validNestedLoop))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(wf.Loops) != 1 {
		t.Fatalf("len(wf.Loops) = %d, want 1", len(wf.Loops))
	}
	lg := wf.Loops[0]
	if lg.ID != "work" {
		t.Errorf("LoopGroup.ID = %q, want work", lg.ID)
	}
	if lg.UntilEmpty != "drain" {
		t.Errorf("LoopGroup.UntilEmpty = %q, want drain", lg.UntilEmpty)
	}
	if lg.Max != 5 {
		t.Errorf("LoopGroup.Max = %d, want 5", lg.Max)
	}
	if !slices.Equal(lg.Members, []workflow.TaskID{"drain", "refine"}) {
		t.Errorf("LoopGroup.Members = %v, want [drain refine]", lg.Members)
	}
}

// TestParse_NestedLoop_TagsTasksWithOwningLoop pins that each Task carries its
// owning loop id, empty for top-level tasks.
func TestParse_NestedLoop_TagsTasksWithOwningLoop(t *testing.T) {
	wf, err := workflow.Parse([]byte(validNestedLoop))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	cases := map[workflow.TaskID]workflow.LoopID{
		"seed":   "",
		"drain":  "work",
		"refine": "work",
	}
	for id, want := range cases {
		task := wf.ByID(id)
		if task == nil {
			t.Fatalf("ByID(%q) = nil", id)
		}
		if task.Loop != want {
			t.Errorf("task %q Loop = %q, want %q", id, task.Loop, want)
		}
	}
}

// TestParse_RejectsLoopIDCollidingWithTask pins that a loop id (wrapper task id)
// equal to a sibling top-level task id is rejected with LoopIDCollisionError
// (Kind "task").
func TestParse_RejectsLoopIDCollidingWithTask(t *testing.T) {
	src := `
name: wf_c
runtime: test-rt
model: m1
tasks:
  - id: work
    prompt: W
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks:
        - id: a
          prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.LoopIDCollisionError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As LoopIDCollisionError failed; err = %v", err)
	}
	if got.Kind != "task" {
		t.Errorf("LoopIDCollisionError.Kind = %q, want task", got.Kind)
	}
}

// TestParse_RejectsLoopIDCollidingWithParam pins that a loop id equal to a param
// name is rejected with LoopIDCollisionError (Kind "param").
func TestParse_RejectsLoopIDCollidingWithParam(t *testing.T) {
	src := `
name: wf_pc
runtime: test-rt
model: m1
params:
  - name: work
    default: x
tasks:
  - id: t
    prompt: use {{params.work}}
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks:
        - id: a
          prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.LoopIDCollisionError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As LoopIDCollisionError failed; err = %v", err)
	}
	if got.Kind != "param" {
		t.Errorf("LoopIDCollisionError.Kind = %q, want param", got.Kind)
	}
}

// TestParse_RejectsDuplicateLoopID pins that two loop tasks sharing the same
// wrapper id are rejected with DuplicateLoopIDError.
func TestParse_RejectsDuplicateLoopID(t *testing.T) {
	src := `
name: wf_dl
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks:
        - id: a
          prompt: A
  - id: work
    loop:
      until_empty: b
      max: 2
      tasks:
        - id: b
          prompt: B
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.DuplicateLoopIDError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As DuplicateLoopIDError failed; err = %v", err)
	}
	if got.Loop != "work" {
		t.Errorf("DuplicateLoopIDError.Loop = %q, want work", got.Loop)
	}
}

// TestParse_RejectsDuplicateTaskIDAcrossLoops pins global task-id uniqueness:
// a nested task id duplicating a top-level task id is rejected with
// DuplicateTaskIDError.
func TestParse_RejectsDuplicateTaskIDAcrossLoops(t *testing.T) {
	src := `
name: wf_dt
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
  - id: work
    loop:
      until_empty: b
      max: 2
      tasks:
        - id: a
          prompt: dup
        - id: b
          depends_on: [a]
          prompt: B {{a}}
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.DuplicateTaskIDError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As DuplicateTaskIDError failed; err = %v", err)
	}
	if got.ID != "a" {
		t.Errorf("DuplicateTaskIDError.ID = %q, want a", got.ID)
	}
}

// TestParse_RejectsEmptyLoopBody pins that a loop with no body tasks is rejected
// with EmptyLoopError.
func TestParse_RejectsEmptyLoopBody(t *testing.T) {
	src := `
name: wf_e
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks: []
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.EmptyLoopError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As EmptyLoopError failed; err = %v", err)
	}
	if got.Loop != "work" {
		t.Errorf("EmptyLoopError.Loop = %q, want work", got.Loop)
	}
}

// TestParse_AcceptsDisconnectedLoopSubgraph pins that a loop body is an ordinary
// DAG: members sharing no edge (the induced subgraph is not connected) are
// accepted, so independent members run in parallel within a pass just like
// independent top-level tasks.
func TestParse_AcceptsDisconnectedLoopSubgraph(t *testing.T) {
	src := `
name: wf_dc
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks:
        - id: a
          prompt: A
        - id: b
          prompt: B
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse rejected a disconnected loop body: %v", err)
	}
	if len(wf.Loops) != 1 {
		t.Fatalf("len(Loops) = %d, want 1", len(wf.Loops))
	}
	if want := []workflow.TaskID{"a", "b"}; !slices.Equal(wf.Loops[0].Members, want) {
		t.Errorf("Members = %v, want %v", wf.Loops[0].Members, want)
	}
}

// TestParse_RejectsTopLevelLoopsBlock pins that the removed top-level `loops:`
// key is now rejected: loops are declared inline as tasks carrying a `loop:`
// block, and KnownFields(true) rejects a stray top-level `loops:` as unknown.
func TestParse_RejectsTopLevelLoopsBlock(t *testing.T) {
	src := `
name: wf_old
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
loops:
  - id: work
    until_empty: a
    max: 2
    tasks:
      - id: a
        prompt: A
`
	_, err := workflow.Parse([]byte(src))
	if err == nil {
		t.Fatalf("Parse accepted a top-level loops: block, want rejection")
	}
	if !strings.Contains(err.Error(), "loops") {
		t.Errorf("error = %v, want it to mention the unknown loops field", err)
	}
}

// TestParse_AcceptsPartiallyConnectedLoopSubgraph pins that a mixed body is also
// fine: a 3-member loop where two members share an edge (a <- b) and the third
// (c) is independent is accepted. b runs after a; c runs in parallel with both.
func TestParse_AcceptsPartiallyConnectedLoopSubgraph(t *testing.T) {
	src := `
name: wf_pc3
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks:
        - id: a
          prompt: A
        - id: b
          depends_on: [a]
          prompt: B {{a}}
        - id: c
          prompt: C
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse rejected a partially connected loop body: %v", err)
	}
	if want := []workflow.TaskID{"a", "b", "c"}; !slices.Equal(wf.Loops[0].Members, want) {
		t.Errorf("Members = %v, want %v", wf.Loops[0].Members, want)
	}
}

// TestParse_RejectsConvergenceTargetNotMember pins that an until_empty naming a
// task outside the loop (here a top-level task) is rejected with
// LoopTargetNotMemberError.
func TestParse_RejectsConvergenceTargetNotMember(t *testing.T) {
	src := `
name: wf_tm
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
  - id: work
    loop:
      until_empty: seed
      max: 2
      tasks:
        - id: a
          prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.LoopTargetNotMemberError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As LoopTargetNotMemberError failed; err = %v", err)
	}
	if got.Task != "seed" {
		t.Errorf("LoopTargetNotMemberError.Task = %q, want seed", got.Task)
	}
}

// TestParse_RejectsLoopMaxBelowOne pins that a loop max < 1 is rejected with
// InvalidLoopGroupMaxError.
func TestParse_RejectsLoopMaxBelowOne(t *testing.T) {
	src := `
name: wf_m
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
  - id: work
    loop:
      until_empty: a
      max: 0
      tasks:
        - id: a
          prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.InvalidLoopGroupMaxError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As InvalidLoopGroupMaxError failed; err = %v", err)
	}
	if got.Loop != "work" {
		t.Errorf("InvalidLoopGroupMaxError.Loop = %q, want work", got.Loop)
	}
}

// TestParse_RejectsBothUntilEmptyAndUntil pins that a loop setting both
// until_empty and until (not exactly one) is rejected with LoopConvergenceError.
func TestParse_RejectsBothUntilEmptyAndUntil(t *testing.T) {
	src := `
name: wf_both
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
  - id: work
    loop:
      until_empty: a
      until: a == "done"
      max: 2
      tasks:
        - id: a
          prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.LoopConvergenceError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As LoopConvergenceError failed; err = %v", err)
	}
}

// TestParse_RejectsNeitherUntilEmptyNorUntil pins that a loop setting neither
// until_empty nor until (not exactly one) is rejected with LoopConvergenceError.
func TestParse_RejectsNeitherUntilEmptyNorUntil(t *testing.T) {
	src := `
name: wf_neither
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: S
  - id: work
    loop:
      max: 2
      tasks:
        - id: a
          prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.LoopConvergenceError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As LoopConvergenceError failed; err = %v", err)
	}
}

// TestParse_RejectsPrevOutsideLoopBody pins that a {{prev.id}} placeholder in a
// top-level (non-loop) task is rejected with PrevOutsideLoopError.
func TestParse_RejectsPrevOutsideLoopBody(t *testing.T) {
	src := `
name: wf_po
runtime: test-rt
model: m1
tasks:
  - id: t
    prompt: bad {{prev.a}}
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks:
        - id: a
          prompt: A
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.PrevOutsideLoopError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As PrevOutsideLoopError failed; err = %v", err)
	}
	if got.Task != "t" {
		t.Errorf("PrevOutsideLoopError.Task = %q, want t", got.Task)
	}
}

// TestParse_RejectsPrevReferencingNonMember pins that a {{prev.id}} placeholder
// inside a loop body that references a task outside that loop is rejected with
// PrevNotMemberError.
func TestParse_RejectsPrevReferencingNonMember(t *testing.T) {
	src := `
name: wf_pn
runtime: test-rt
model: m1
tasks:
  - id: outside
    prompt: O
  - id: work
    loop:
      until_empty: a
      max: 2
      tasks:
        - id: a
          prompt: A {{prev.outside}}
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.PrevNotMemberError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As PrevNotMemberError failed; err = %v", err)
	}
	if got.Name != "outside" {
		t.Errorf("PrevNotMemberError.Name = %q, want outside", got.Name)
	}
}
