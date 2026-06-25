package workflow_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParseForEachStatic pins that a `for_each:` block with a literal `in:`
// sequence folds into a LoopForEach group: a static List, empty ListSource, the
// declared loop variable, and the nested tasks as members.
func TestParseForEachStatic(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: [redis, postgres, nats]
      as: backend
      tasks:
        - id: handle
          prompt: probe {{backend}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(wf.Loops) != 1 {
		t.Fatalf("len(Loops) = %d, want 1", len(wf.Loops))
	}
	lg := wf.Loops[0]
	if lg.ID != "probe" {
		t.Errorf("Loop ID = %q, want probe", lg.ID)
	}
	if lg.Kind != workflow.LoopForEach {
		t.Errorf("Loop Kind = %v, want LoopForEach", lg.Kind)
	}
	if lg.As != "backend" {
		t.Errorf("As = %q, want backend", lg.As)
	}
	if lg.ListSource != "" {
		t.Errorf("ListSource = %q, want empty", lg.ListSource)
	}
	if want := []string{"redis", "postgres", "nats"}; !slices.Equal(lg.List, want) {
		t.Errorf("List = %v, want %v", lg.List, want)
	}
	if want := []workflow.TaskID{"handle"}; !slices.Equal(lg.Members, want) {
		t.Errorf("Members = %v, want %v", lg.Members, want)
	}
	if got := wf.ByID("handle"); got == nil || got.Loop != "probe" {
		t.Errorf("handle.Loop = %v, want probe", got)
	}
}

// TestParseForEachDynamic pins that a single-placeholder `in:` scalar folds into
// ListSource (dynamic list) with a nil List; the source task need not be a
// member dependency (the loop takes it as an entry dependency).
func TestParseForEachDynamic(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: discover
    prompt: list bugs
  - id: fix
    for_each:
      in: "{{discover}}"
      as: bug
      tasks:
        - id: apply
          prompt: fix {{bug}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	lg := wf.Loops[0]
	if lg.List != nil {
		t.Errorf("List = %v, want nil", lg.List)
	}
	if lg.ListSource != "{{discover}}" {
		t.Errorf("ListSource = %q, want {{discover}}", lg.ListSource)
	}
	if lg.As != "bug" {
		t.Errorf("As = %q, want bug", lg.As)
	}
}

// TestParseForEachLoopVarNotADep pins that the `as` loop variable in a member
// body is whitelisted: {{bug}} must not be rejected as an undeclared dependency
// the way a stray task placeholder would be.
func TestParseForEachLoopVarNotADep(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: [a, b]
      as: item
      tasks:
        - id: handle
          prompt: handle {{item}}
`
	if _, err := workflow.Parse([]byte(src)); err != nil {
		t.Fatalf("Parse rejected whitelisted loop variable: %v", err)
	}
}

// TestParseForEachMissingAs pins that a `for_each:` block without `as:` is
// rejected.
func TestParseForEachMissingAs(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: [a, b]
      tasks:
        - id: handle
          prompt: handle it
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.MissingForEachVarError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As MissingForEachVarError failed; err = %v", err)
	}
}

// TestParseForEachMissingIn pins that a `for_each:` block without `in:` is
// rejected.
func TestParseForEachMissingIn(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      as: item
      tasks:
        - id: handle
          prompt: handle {{item}}
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.MissingForEachListError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As MissingForEachListError failed; err = %v", err)
	}
}

// TestParseLoopAndForEachSet pins that a task declaring both a `loop:` and a
// `for_each:` block is rejected: the two are sibling scoped-block forms.
func TestParseLoopAndForEachSet(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: both
    loop:
      until_empty: w
      max: 2
      tasks:
        - id: w
          prompt: while
    for_each:
      in: [a]
      as: item
      tasks:
        - id: f
          prompt: each {{item}}
`
	if _, err := workflow.Parse([]byte(src)); !errors.Is(err, workflow.ErrLoopAndForEachSet) {
		t.Fatalf("Parse error = %v, want ErrLoopAndForEachSet", err)
	}
}

// TestParseForEachAsCollision pins that an `as:` colliding with a task id or a
// param name is rejected with ForEachVarCollisionError.
func TestParseForEachAsCollision(t *testing.T) {
	taskCollision := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: [a, b]
      as: seed
      tasks:
        - id: handle
          prompt: handle {{seed}}
`
	paramCollision := `
name: wf
runtime: test-rt
model: m1
params:
  - name: env
    default: prod
tasks:
  - id: seed
    prompt: seed {{params.env}}
  - id: probe
    for_each:
      in: [a, b]
      as: env
      tasks:
        - id: handle
          prompt: handle {{env}}
`
	for name, src := range map[string]string{"task": taskCollision, "param": paramCollision} {
		_, err := workflow.Parse([]byte(src))
		var got *workflow.ForEachVarCollisionError
		if !errors.As(err, &got) {
			t.Fatalf("%s: errors.As ForEachVarCollisionError failed; err = %v", name, err)
		}
		if got.Kind != name {
			t.Errorf("%s: ForEachVarCollisionError.Kind = %q, want %q", name, got.Kind, name)
		}
	}
}

// TestParseForEachInvalidAs pins that an `as:` outside the identifier alphabet
// is rejected.
func TestParseForEachInvalidAs(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: [a, b]
      as: bad-name
      tasks:
        - id: handle
          prompt: handle it
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.InvalidForEachVarError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As InvalidForEachVarError failed; err = %v", err)
	}
}

// TestParseForEachDynamicSourceUnknown pins that a dynamic `in:` source naming a
// task the workflow does not declare is rejected.
func TestParseForEachDynamicSourceUnknown(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: fix
    for_each:
      in: "{{nope}}"
      as: bug
      tasks:
        - id: apply
          prompt: fix {{bug}}
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownForEachListRefError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownForEachListRefError failed; err = %v", err)
	}
}

// TestParseForEachInvalidSource pins that a scalar `in:` that is not a single
// `{{...}}` placeholder is rejected with InvalidForEachListError.
func TestParseForEachInvalidSource(t *testing.T) {
	for _, bad := range []string{"just text", "{{a}} and {{b}}"} {
		src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: fix
    for_each:
      in: "` + bad + `"
      as: item
      tasks:
        - id: apply
          prompt: fix {{item}}
`
		_, err := workflow.Parse([]byte(src))
		var got *workflow.InvalidForEachListError
		if !errors.As(err, &got) {
			t.Fatalf("in=%q: errors.As InvalidForEachListError failed; err = %v", bad, err)
		}
	}
}

// TestParseForEachDynamicStateSource pins that a `{{state.x}}` dynamic source is
// accepted without any task reference (state refs create no DAG edge).
func TestParseForEachDynamicStateSource(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: fix
    for_each:
      in: "{{state.backlog}}"
      as: item
      tasks:
        - id: apply
          prompt: fix {{item}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got := wf.Loops[0].ListSource; got != "{{state.backlog}}" {
		t.Errorf("ListSource = %q, want {{state.backlog}}", got)
	}
}

// TestParseForEachParallel pins that a `for_each_parallel:` block folds into the
// same LoopForEach group as `for_each:` but with Parallel set: it shares the
// in/as/tasks shape and all the static/dynamic resolution.
func TestParseForEachParallel(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each_parallel:
      in: [redis, postgres, nats]
      as: backend
      tasks:
        - id: handle
          prompt: probe {{backend}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	lg := wf.Loops[0]
	if lg.Kind != workflow.LoopForEach {
		t.Errorf("Loop Kind = %v, want LoopForEach", lg.Kind)
	}
	if !lg.Parallel {
		t.Errorf("Parallel = false, want true")
	}
	if lg.As != "backend" {
		t.Errorf("As = %q, want backend", lg.As)
	}
	if want := []string{"redis", "postgres", "nats"}; !slices.Equal(lg.List, want) {
		t.Errorf("List = %v, want %v", lg.List, want)
	}
}

// TestParseForEachParallelSequentialNotParallel pins that a plain `for_each:`
// leaves Parallel false, so the flag is opt-in to the new spelling.
func TestParseForEachParallelSequentialNotParallel(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: [a, b]
      as: item
      tasks:
        - id: handle
          prompt: handle {{item}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.Loops[0].Parallel {
		t.Errorf("Parallel = true for a sequential for_each, want false")
	}
}

// TestParseLoopAndForEachParallelSet pins that a task declaring both a `loop:`
// and a `for_each_parallel:` block is rejected like the loop+for_each conflict.
func TestParseLoopAndForEachParallelSet(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: both
    loop:
      until_empty: w
      max: 2
      tasks:
        - id: w
          prompt: while
    for_each_parallel:
      in: [a]
      as: item
      tasks:
        - id: f
          prompt: each {{item}}
`
	if _, err := workflow.Parse([]byte(src)); !errors.Is(err, workflow.ErrLoopAndForEachSet) {
		t.Fatalf("Parse error = %v, want ErrLoopAndForEachSet", err)
	}
}

// TestParseForEachAndForEachParallelSet pins that a task setting both the
// sequential and parallel for_each spellings is a body-form conflict.
func TestParseForEachAndForEachParallelSet(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: both
    for_each:
      in: [a]
      as: item
      tasks:
        - id: f
          prompt: each {{item}}
    for_each_parallel:
      in: [b]
      as: other
      tasks:
        - id: g
          prompt: each {{other}}
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.TaskBodyConflictError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As TaskBodyConflictError failed; err = %v", err)
	}
}

// TestParseForEachParallelRejectsPrev pins that a `{{prev.id}}` placeholder
// inside a parallel for_each body is rejected: parallel passes have no ordering,
// so there is no prior iteration to read.
func TestParseForEachParallelRejectsPrev(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each_parallel:
      in: [a, b]
      as: item
      tasks:
        - id: handle
          prompt: "handle {{item}} {{prev.handle}}"
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.PrevInParallelLoopError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As PrevInParallelLoopError failed; err = %v", err)
	}
}

// TestParseForEachEmptyTasks pins that a `for_each:` block with no nested tasks
// is rejected with EmptyLoopError, the shared empty-body sentinel.
func TestParseForEachEmptyTasks(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: [a, b]
      as: item
      tasks: []
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.EmptyLoopError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As EmptyLoopError failed; err = %v", err)
	}
}
