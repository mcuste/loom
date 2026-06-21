package executor_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestRunForEachStatic exercises a static fanout: one instance per literal
// value, each binding {{as}}, with the node's joined output visible to a
// downstream task via {{base_id}}.
func TestRunForEachStatic(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: probe
    for_each: [redis, postgres, nats]
    as: backend
    prompt: "probe {{backend}}"
  - id: report
    depends_on: [probe]
    prompt: "results:\n{{probe}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// echoRuntime echoes the prompt; instances run concurrently so order within
	// the joined output is by list position, not completion.
	want := "probe redis\nprobe postgres\nprobe nats"
	if rep.Outputs["probe"] != want {
		t.Errorf("Outputs[probe] = %q, want %q", rep.Outputs["probe"], want)
	}
	if rep.Outputs["report"] != "results:\n"+want {
		t.Errorf("Outputs[report] = %q, want %q", rep.Outputs["report"], "results:\n"+want)
	}

	// Three echo instances, each 10 in / 20 out, plus the report task: 40 in /
	// 80 out total. Usage is summed across instances.
	if rep.Usage.InputTokens != 40 || rep.Usage.OutputTokens != 80 {
		t.Errorf("Usage = %+v, want 40 in / 80 out", rep.Usage)
	}
}

// TestRunForEachDynamicNewlines exercises a dynamic fanout whose list comes
// from an upstream shell task as a newline-separated string (with blanks the
// parser must drop).
func TestRunForEachDynamicNewlines(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: discover
    command: "printf 'one\ntwo\n\nthree\n'"
  - id: handle
    for_each: "{{discover}}"
    as: item
    depends_on: [discover]
    prompt: "do {{item}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "do one\ndo two\ndo three"
	if rep.Outputs["handle"] != want {
		t.Errorf("Outputs[handle] = %q, want %q", rep.Outputs["handle"], want)
	}
}

// TestRunForEachDynamicJSON exercises a dynamic fanout whose list comes from an
// upstream task as a JSON array of strings.
func TestRunForEachDynamicJSON(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: discover
    command: "printf '[\"a\", \"b\", \"c\"]'"
  - id: handle
    for_each: "{{discover}}"
    as: item
    depends_on: [discover]
    prompt: "x{{item}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "xa\nxb\nxc"
	if rep.Outputs["handle"] != want {
		t.Errorf("Outputs[handle] = %q, want %q", rep.Outputs["handle"], want)
	}
}

// TestRunForEachEmptyList pins that an empty resolved list produces an empty
// node output and runs no instances (the loop-until-dry "drained" signal).
func TestRunForEachEmptyList(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: discover
    command: "true"
  - id: handle
    for_each: "{{discover}}"
    as: item
    depends_on: [discover]
    prompt: "do {{item}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["handle"] != "" {
		t.Errorf("Outputs[handle] = %q, want empty", rep.Outputs["handle"])
	}
	// The fanout node still completes (fires its hooks / appears in Tasks) even
	// with no instances; only the instances are skipped.
	if got := rep.Outputs["discover"]; got != "" {
		t.Errorf("Outputs[discover] = %q, want empty", got)
	}
}

// TestRunForEachStaticEmpty pins that an explicit empty static list behaves the
// same as an empty dynamic list: empty output, no instances.
func TestRunForEachStaticEmpty(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: probe
    for_each: []
    as: backend
    prompt: "probe {{backend}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["probe"] != "" {
		t.Errorf("Outputs[probe] = %q, want empty", rep.Outputs["probe"])
	}
}

// TestRunForEachShell exercises a shell fanout, confirming {{as}} substitutes
// into the command body per instance.
func TestRunForEachShell(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: each
    for_each: [1, 2, 3]
    as: n
    command: "echo n={{n}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Instances run concurrently; assert the set of lines regardless of order.
	got := strings.Split(rep.Outputs["each"], "\n")
	sort.Strings(got)
	want := []string{"n=1", "n=2", "n=3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Outputs[each] lines = %v, want %v", got, want)
	}
}

// TestRunForEachInstanceError pins that one failing instance fails the whole
// fanout node and propagates the error from Run.
func TestRunForEachInstanceError(t *testing.T) {
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: each
    for_each: [ok1, ok2]
    as: n
    command: "test {{n}} = ok1"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// "test ok2 = ok1" exits non-zero, so one instance fails the node.
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err == nil {
		t.Fatal("Run returned nil error, want failure from a failing instance")
	}
}
