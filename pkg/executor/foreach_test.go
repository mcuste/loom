package executor_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

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

// TestRunForEachParallelAllItems pins that a `for_each_parallel:` block runs its
// body once per element: every element's prompt is produced (order-independent,
// since passes race) and each pass is stamped with its 1-based number.
func TestRunForEachParallelAllItems(t *testing.T) {
	t.Parallel()
	rt, rec := newLoopRT(t, nil, runtime.Usage{InputTokens: 10, OutputTokens: 20})

	src := fmt.Sprintf(`
name: wf
runtime: %s
model: m1
tasks:
  - id: seed
    command: "true"
  - id: probe
    for_each_parallel:
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

	// Passes race, so compare as a set rather than a sequence.
	got := rec.promptsFor("handle")
	slices.Sort(got)
	want := []string{"probe nats", "probe postgres", "probe redis"}
	if !slices.Equal(got, want) {
		t.Errorf("handle prompts = %v, want %v (any order)", got, want)
	}
	// Every pass number 1..3 appears exactly once, regardless of completion order.
	its := iterationsOf(rep, "handle")
	slices.Sort(its)
	if !slices.Equal(its, []int{1, 2, 3}) {
		t.Errorf("handle iterations = %v, want [1 2 3]", its)
	}
	// Usage accumulates across every pass, exactly as the sequential loop does.
	if rep.Usage.InputTokens != 30 || rep.Usage.OutputTokens != 60 {
		t.Errorf("Usage = %+v, want 30 in / 60 out", rep.Usage)
	}
}

// TestRunForEachParallelIsolatesMembers pins that each parallel pass runs over an
// isolated copy of the member outputs: a two-member body where b reads sibling a
// must, for every element, observe a's value from its own pass and never another
// pass's. Without per-pass isolation the shared outputs map would let b read a
// different element's a, producing duplicate or missing results.
func TestRunForEachParallelIsolatesMembers(t *testing.T) {
	t.Parallel()
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: seed
    command: "true"
  - id: fan
    for_each_parallel:
      in: [1, 2, 3, 4]
      as: n
      tasks:
        - id: a
          command: "printf 'A{{n}}'"
        - id: b
          depends_on: [a]
          command: "printf '{{a}}B'"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var bs []string
	for _, r := range rep.Tasks {
		if r.TaskID == "b" {
			bs = append(bs, r.Output)
		}
	}
	slices.Sort(bs)
	want := []string{"A1B", "A2B", "A3B", "A4B"}
	if !slices.Equal(bs, want) {
		t.Errorf("b outputs = %v, want %v (each pass sees its own a)", bs, want)
	}
}

// TestRunForEachParallelRunsConcurrently pins that the passes truly run at the
// same time: every pass must enter Run before any is released. The barrier
// blocks each call until the test sees all four arrive; a sequential driver
// would stall on the first pass and the test would time out waiting for the
// rest. seed is a shell task so it bypasses the barrier runtime; only the four
// handle passes arrive.
func TestRunForEachParallelRunsConcurrently(t *testing.T) {
	rt, barrier := newBarrier(t)
	src := `
name: wf
runtime: ` + rt + `
model: m1
tasks:
  - id: seed
    command: "true"
  - id: fan
    for_each_parallel:
      in: [a, b, c, d]
      as: item
      tasks:
        - id: handle
          prompt: "do {{item}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, wf, executor.Hooks{}, executor.Options{})
		done <- err
	}()

	for i := range 4 {
		select {
		case <-barrier.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/4 passes reached Run before timeout: for_each_parallel is serial", i)
		}
	}
	close(barrier.release)

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRunForEachBodyIndependentMembersConcurrent pins that a for_each body whose
// members share no depends_on edge runs those members in parallel within a pass,
// like independent top-level tasks. The barrier blocks each member until both
// have entered Run; a serial body would only ever reach one and time out. The
// single-element list keeps it to one pass so exactly the two members arrive.
func TestRunForEachBodyIndependentMembersConcurrent(t *testing.T) {
	rt, barrier := newBarrier(t)
	src := `
name: wf
runtime: ` + rt + `
model: m1
tasks:
  - id: seed
    command: "true"
  - id: fan
    for_each:
      in: [only]
      as: item
      tasks:
        - id: a
          prompt: "a {{item}}"
        - id: b
          prompt: "b {{item}}"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, wf, executor.Hooks{}, executor.Options{})
		done <- err
	}()

	for i := range 2 {
		select {
		case <-barrier.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/2 for_each members reached Run before timeout: body is serial", i)
		}
	}
	close(barrier.release)

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRunForEachParallelWhenSkipDoesNotClobber pins the merge rule for a
// when-guarded member inside a for_each_parallel body: when the guard skips the
// member for one element (b) but runs it for others (a, c), the published member
// output must be a real value (never the skipped element's empty output) and the
// member must be reported succeeded, not skipped. Without the success-dominates
// merge, a racing skip pass could clobber the real output with "" while leaving
// succeeded(handle) true, publishing an empty value to the exit consumer.
func TestRunForEachParallelWhenSkipDoesNotClobber(t *testing.T) {
	t.Parallel()
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: fan
    for_each_parallel:
      in: [a, b, c]
      as: x
      tasks:
        - id: gen
          command: "printf '%s' '{{x}}'"
        - id: handle
          depends_on: [gen]
          when: '{{gen}} != "b"'
          command: "printf 'do %s' '{{gen}}'"
  - id: report
    depends_on: [handle]
    when: succeeded(handle)
    command: "printf 'got:%s' '{{handle}}'"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// succeeded(handle) is true (some element ran it), so report runs; its output
	// embeds a real handle value, never the skipped element's empty string.
	got := rep.Outputs["report"]
	if got != "got:do a" && got != "got:do c" {
		t.Errorf("Outputs[report] = %q, want got:do a or got:do c (no empty clobber, report ran)", got)
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
