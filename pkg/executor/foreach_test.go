package executor_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestRunForEachStatic drives a static `for_each:` block: the body member runs
// once per literal value, sequentially and in list order, with the loop
// variable bound into each pass's prompt. The member's report Output is the last
// iteration's value (loop semantics), not a join.
func TestRunForEachStatic(t *testing.T) {
	t.Parallel()
	rt, rec := newLoopRT(t, map[string][]string{
		"handle": {"r1", "r2", "r3"},
	}, runtime.Usage{InputTokens: 10, OutputTokens: 20})

	src := fmt.Sprintf(`
name: wf
runtime: %s
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
          depends_on: [seed]
          prompt: "probe {{backend}}"
`, rt)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Prompts are recorded in call order; a sequential for_each over the list
	// produces exactly these, in order.
	got := rec.promptsFor("handle")
	want := []string{"probe redis", "probe postgres", "probe nats"}
	if !slices.Equal(got, want) {
		t.Errorf("handle prompts = %v, want %v", got, want)
	}
	// The member's published output is the final iteration's value.
	if rep.Outputs["handle"] != "r3" {
		t.Errorf("Outputs[handle] = %q, want r3", rep.Outputs["handle"])
	}
	// One pass per list element, stamped 1..3.
	if its := iterationsOf(rep, "handle"); !slices.Equal(its, []int{1, 2, 3}) {
		t.Errorf("handle iterations = %v, want [1 2 3]", its)
	}
}

// TestRunForEachDynamicNewlines drives a dynamic list sourced from an upstream
// shell task as a newline-separated string (with blanks the parser drops).
func TestRunForEachDynamicNewlines(t *testing.T) {
	t.Parallel()
	rt, rec := newLoopRT(t, nil, runtime.Usage{})

	src := fmt.Sprintf(`
name: wf
runtime: %s
model: m1
tasks:
  - id: discover
    command: "printf 'one\ntwo\n\nthree\n'"
  - id: fan
    for_each:
      in: "{{discover}}"
      as: item
      tasks:
        - id: handle
          prompt: "do {{item}}"
`, rt)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rec.promptsFor("handle")
	want := []string{"do one", "do two", "do three"}
	if !slices.Equal(got, want) {
		t.Errorf("handle prompts = %v, want %v", got, want)
	}
}

// TestRunForEachDynamicJSON drives a dynamic list sourced from an upstream shell
// task as a JSON array of strings.
func TestRunForEachDynamicJSON(t *testing.T) {
	t.Parallel()
	rt, rec := newLoopRT(t, nil, runtime.Usage{})

	src := fmt.Sprintf(`
name: wf
runtime: %s
model: m1
tasks:
  - id: discover
    command: "printf '[\"a\", \"b\", \"c\"]'"
  - id: fan
    for_each:
      in: "{{discover}}"
      as: item
      tasks:
        - id: handle
          prompt: "x{{item}}"
`, rt)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rec.promptsFor("handle")
	want := []string{"xa", "xb", "xc"}
	if !slices.Equal(got, want) {
		t.Errorf("handle prompts = %v, want %v", got, want)
	}
}

// TestRunForEachEmptyList pins that an empty resolved list runs zero iterations:
// the member never runs, yet the loop closes its member gate so a downstream
// consumer proceeds against the empty output.
func TestRunForEachEmptyList(t *testing.T) {
	t.Parallel()
	rt, rec := newLoopRT(t, map[string][]string{"report": {"REPORT"}}, runtime.Usage{})

	src := fmt.Sprintf(`
name: wf
runtime: %s
model: m1
tasks:
  - id: discover
    command: "true"
  - id: fan
    for_each:
      in: "{{discover}}"
      as: item
      tasks:
        - id: handle
          prompt: "do {{item}}"
  - id: report
    depends_on: [handle]
    prompt: "got:{{handle}}"
`, rt)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rec.promptsFor("handle"); len(got) != 0 {
		t.Errorf("handle prompts = %v, want none (zero iterations)", got)
	}
	if rep.Outputs["report"] != "REPORT" {
		t.Errorf("Outputs[report] = %q, want REPORT (consumer ran past empty loop)", rep.Outputs["report"])
	}
}

// TestRunForEachStaticEmpty pins that an explicit empty static list behaves the
// same as an empty dynamic list: zero iterations, member never runs.
func TestRunForEachStaticEmpty(t *testing.T) {
	t.Parallel()
	rt, rec := newLoopRT(t, nil, runtime.Usage{})

	src := fmt.Sprintf(`
name: wf
runtime: %s
model: m1
tasks:
  - id: seed
    prompt: seed
  - id: probe
    for_each:
      in: []
      as: backend
      tasks:
        - id: handle
          prompt: "probe {{backend}}"
`, rt)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rec.promptsFor("handle"); len(got) != 0 {
		t.Errorf("handle prompts = %v, want none (zero iterations)", got)
	}
}

// TestRunForEachShell drives a shell body member: the loop variable substitutes
// into the command per pass, and `{{prev.acc}}` accumulates across the
// sequential passes, proving in-order execution.
func TestRunForEachShell(t *testing.T) {
	t.Parallel()
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: seed
    command: "true"
  - id: loop
    for_each:
      in: [1, 2, 3]
      as: n
      tasks:
        - id: acc
          command: "printf '%s%s' '{{prev.acc}}' '{{n}}'"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Outputs["acc"] != "123" {
		t.Errorf("Outputs[acc] = %q, want 123 (sequential accumulation)", rep.Outputs["acc"])
	}
}

// TestRunForEachMemberError pins that a failing pass fails the whole loop and
// propagates the error from Run.
func TestRunForEachMemberError(t *testing.T) {
	t.Parallel()
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: seed
    command: "true"
  - id: loop
    for_each:
      in: [ok1, ok2]
      as: n
      tasks:
        - id: check
          command: "test {{n}} = ok1"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// The second pass ("test ok2 = ok1") exits non-zero, failing the loop.
	if _, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{}); err == nil {
		t.Fatal("Run returned nil error, want failure from a failing pass")
	}
}
