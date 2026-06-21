package workflow_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestParseForEachStatic pins that a literal `for_each:` sequence parses into
// the task's ForEach values with an empty ForEachSource (static fanout).
func TestParseForEachStatic(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: probe
    for_each: [redis, postgres, nats]
    as: backend
    prompt: probe {{backend}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	probe := wf.ByID("probe")
	if !probe.IsForEach() {
		t.Fatal("probe.IsForEach() = false, want true")
	}
	if probe.ForEachSource != "" {
		t.Errorf("ForEachSource = %q, want empty", probe.ForEachSource)
	}
	if probe.As != "backend" {
		t.Errorf("As = %q, want backend", probe.As)
	}
	if want := []string{"redis", "postgres", "nats"}; !slices.Equal(probe.ForEach, want) {
		t.Errorf("ForEach = %v, want %v", probe.ForEach, want)
	}
}

// TestParseForEachDynamic pins that a single-placeholder scalar parses into
// ForEachSource (dynamic fanout) with a nil ForEach, and that the source task
// must be declared in depends_on.
func TestParseForEachDynamic(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: discover
    prompt: list bugs
  - id: fix
    for_each: "{{discover}}"
    as: bug
    depends_on: [discover]
    prompt: fix {{bug}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	fix := wf.ByID("fix")
	if !fix.IsForEach() {
		t.Fatal("fix.IsForEach() = false, want true")
	}
	if fix.ForEach != nil {
		t.Errorf("ForEach = %v, want nil", fix.ForEach)
	}
	if fix.ForEachSource != "{{discover}}" {
		t.Errorf("ForEachSource = %q, want {{discover}}", fix.ForEachSource)
	}
	if fix.As != "bug" {
		t.Errorf("As = %q, want bug", fix.As)
	}
}

// TestParseForEachAsPlaceholderNotADep pins that the `as` loop variable in the
// prompt is whitelisted: {{bug}} must not be rejected as an undeclared
// dependency the way a stray task placeholder would be.
func TestParseForEachAsPlaceholderNotADep(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: probe
    for_each: [a, b]
    as: item
    prompt: handle {{item}}
`
	if _, err := workflow.Parse([]byte(src)); err != nil {
		t.Fatalf("Parse rejected whitelisted as-variable: %v", err)
	}
}

// TestParseForEachMissingAs pins that a `for_each:` without `as:` is rejected.
func TestParseForEachMissingAs(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: probe
    for_each: [a, b]
    prompt: handle it
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.MissingForEachAsError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As MissingForEachAsError failed; err = %v", err)
	}
}

// TestParseAsWithoutForEach pins that an `as:` declared without a `for_each:` is
// rejected — the loop variable would bind nothing.
func TestParseAsWithoutForEach(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: probe
    as: item
    prompt: handle it
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.ForEachAsWithoutForEachError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As ForEachAsWithoutForEachError failed; err = %v", err)
	}
}

// TestParseForEachAsCollision pins that an `as:` colliding with a task id or a
// param name is rejected with ForEachAsCollisionError.
func TestParseForEachAsCollision(t *testing.T) {
	taskCollision := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: probe
    for_each: [a, b]
    as: probe
    prompt: handle {{probe}}
`
	paramCollision := `
name: wf
runtime: test-rt
model: m1
params:
  - name: env
    default: prod
tasks:
  - id: probe
    for_each: [a, b]
    as: env
    prompt: handle {{env}} {{params.env}}
`
	for name, src := range map[string]string{"task": taskCollision, "param": paramCollision} {
		_, err := workflow.Parse([]byte(src))
		var got *workflow.ForEachAsCollisionError
		if !errors.As(err, &got) {
			t.Fatalf("%s: errors.As ForEachAsCollisionError failed; err = %v", name, err)
		}
		if got.Kind != name {
			t.Errorf("%s: ForEachAsCollisionError.Kind = %q, want %q", name, got.Kind, name)
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
  - id: probe
    for_each: [a, b]
    as: bad-name
    prompt: handle it
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.InvalidForEachAsError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As InvalidForEachAsError failed; err = %v", err)
	}
}

// TestParseForEachDynamicSourceNotADep pins that a `{{id}}` dynamic source
// naming a task absent from depends_on is rejected (reusing the depends_on
// placeholder rule).
func TestParseForEachDynamicSourceNotADep(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: discover
    prompt: list bugs
  - id: fix
    for_each: "{{discover}}"
    as: bug
    prompt: fix {{bug}}
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownPlaceholderError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownPlaceholderError failed; err = %v", err)
	}
}

// TestParseForEachInvalidSource pins that a scalar `for_each:` that is not a
// single `{{...}}` placeholder is rejected with InvalidForEachSourceError.
func TestParseForEachInvalidSource(t *testing.T) {
	for _, bad := range []string{"just text", "{{a}} and {{b}}"} {
		src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
  - id: b
    prompt: B
  - id: fix
    for_each: "` + bad + `"
    as: item
    depends_on: [a, b]
    prompt: fix {{item}}
`
		_, err := workflow.Parse([]byte(src))
		var got *workflow.InvalidForEachSourceError
		if !errors.As(err, &got) {
			t.Fatalf("for_each=%q: errors.As InvalidForEachSourceError failed; err = %v", bad, err)
		}
	}
}

// TestParseForEachDynamicStateSource pins that a `{{state.x}}` dynamic source is
// accepted without a depends_on entry (state refs create no DAG edge).
func TestParseForEachDynamicStateSource(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: fix
    for_each: "{{state.backlog}}"
    as: item
    prompt: fix {{item}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if got := wf.ByID("fix").ForEachSource; got != "{{state.backlog}}" {
		t.Errorf("ForEachSource = %q, want {{state.backlog}}", got)
	}
}
