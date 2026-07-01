package runner

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
)

// TestRunWorkflow_PlainRunCompletesWithoutSeededLine pins that calling the
// unified pipeline with a zero SeedPlan runs every task and prints the plain
// summary, with no "Seeded" line anywhere in the output. This guards the
// byte-identity of a plain `loom run` after the de-duplication.
func TestRunWorkflow_PlainRunCompletesWithoutSeededLine(t *testing.T) {
	home := runnerTestHome(t)
	chdirTo(t, t.TempDir())

	manifest := `name: wf
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello world
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	var buf bytes.Buffer
	if _, err := Run(context.Background(), newTestObserver(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home}); err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "Seeded   :") {
		t.Errorf("plain run emitted a Seeded line:\n%s", out)
	}
	if !strings.Contains(out, `✓ workflow "wf" complete`) {
		t.Errorf("plain run did not run to completion:\n%s", out)
	}
}

// TestRunWorkflow_SeededRunPrintsSeededCount pins that a non-empty SeedPlan
// prints the "Seeded   : N task(s) from prior run" line with the seed count.
func TestRunWorkflow_SeededRunPrintsSeededCount(t *testing.T) {
	home := runnerTestHome(t)
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-fail
    prompt: would-fail-if-rerun
  - id: b
    runtime: cmd-echo
    depends_on: [a]
    prompt: "got: {{a}}"
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	plan := SeedPlan{
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if _, err := Run(context.Background(), newTestObserver(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
	}
	if !strings.Contains(buf.String(), "Seeded   : 1 task(s) from prior run") {
		t.Errorf("seeded run did not print the Seeded count line:\n%s", buf.String())
	}
}

// TestRunWorkflow_SeededRunSkipsSeededTask pins that a seeded task is never
// re-dispatched: `a` is wired to cmd-fail, so the run would error if the
// executor ran it. Success plus the downstream prompt carrying the seed value
// proves the seed bypassed the runtime and fed `b` via substitution.
func TestRunWorkflow_SeededRunSkipsSeededTask(t *testing.T) {
	home := runnerTestHome(t)
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-fail
    prompt: would-fail-if-rerun
  - id: b
    runtime: cmd-echo
    depends_on: [a]
    prompt: "got: {{a}}"
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	plan := SeedPlan{
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if _, err := Run(context.Background(), newTestObserver(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
	}

	rec := readNewRun(t, home, "wf", "")
	bPrompt, _ := taskField(t, rec, "b", "prompt")
	if bPrompt != "got: stored-a" {
		t.Errorf("b.prompt = %q, want %q (seed of a did not bypass runtime and feed downstream)", bPrompt, "got: stored-a")
	}
}

// TestRunWorkflow_SeededRunStampsSeedIntoRecord pins that each seeded entry is
// stamped into the fresh run record as an already-ok task before the executor
// starts, so a later resume of this run finds it complete rather than
// re-dispatching it.
func TestRunWorkflow_SeededRunStampsSeedIntoRecord(t *testing.T) {
	home := runnerTestHome(t)
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-fail
    prompt: would-fail-if-rerun
  - id: b
    runtime: cmd-echo
    depends_on: [a]
    prompt: "got: {{a}}"
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	plan := SeedPlan{
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if _, err := Run(context.Background(), newTestObserver(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
	}

	rec := readNewRun(t, home, "wf", "")
	aOutput, sawA := taskField(t, rec, "a", "output")
	if !sawA {
		t.Fatalf("seeded task `a` was not stamped into the new run record")
	}
	if aOutput != "stored-a" {
		t.Errorf("stamped a.output = %q, want %q", aOutput, "stored-a")
	}
}

// TestRunWorkflow_SeededRunReducesExpectedCount pins that the executor is told
// to run only the remainder: expected = len(tasks) - len(seed). With 2 tasks
// and 1 seed, the per-task progress denominator must read /1, not /2.
func TestRunWorkflow_SeededRunReducesExpectedCount(t *testing.T) {
	home := runnerTestHome(t)
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
tasks:
  - id: a
    runtime: cmd-fail
    prompt: would-fail-if-rerun
  - id: b
    runtime: cmd-echo
    depends_on: [a]
    prompt: "got: {{a}}"
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	plan := SeedPlan{
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if _, err := Run(context.Background(), newTestObserver(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "[1/1]") {
		t.Errorf("expected progress denominator /1 (expected = tasks - seed); got:\n%s", out)
	}
}

// TestRunWorkflow_SeededEntryAbsentFromWorkflowIsDropped pins that a seed
// entry whose id no longer resolves in the current workflow is dropped rather
// than dereferenced: wf.ByID returns nil for "ghost", so stamping it would
// panic. The run must complete normally, the ghost must not inflate the
// expected count (denominator stays /1, not /0), and no record is stamped for
// it.
func TestRunWorkflow_SeededEntryAbsentFromWorkflowIsDropped(t *testing.T) {
	home := runnerTestHome(t)
	chdirTo(t, t.TempDir())

	manifest := `name: wf
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello world
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	plan := SeedPlan{
		entries: []seedEntry{{id: "ghost", prompt: "p", output: "stored-ghost"}},
	}

	var buf bytes.Buffer
	if _, err := Run(context.Background(), newTestObserver(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "Seeded   :") {
		t.Errorf("out-of-workflow seed should not print a Seeded line:\n%s", out)
	}
	if !strings.Contains(out, "[1/1]") {
		t.Errorf("ghost seed must not reduce expected count below the real task count; got:\n%s", out)
	}
	if !strings.Contains(out, `✓ workflow "wf" complete`) {
		t.Errorf("run did not complete:\n%s", out)
	}

	rec := readNewRun(t, home, "wf", "")
	if _, ok := taskField(t, rec, "ghost", "output"); ok {
		t.Errorf("ghost seed was stamped into the run record but its id is absent from the workflow")
	}
}

// TestRunWorkflow_SurfacesBudgetExceeded pins that when the executor aborts on
// the workflow cost budget, Run surfaces the typed executor.BudgetExceededError
// to the caller rather than swallowing it. The three-task chain at cost 0.5
// each overruns the 0.75 budget before its last task is dispatched.
func TestRunWorkflow_SurfacesBudgetExceeded(t *testing.T) {
	home := runnerTestHome(t)
	chdirTo(t, t.TempDir())

	manifest := `name: wf
runtime: cmd-cost
model: m1
budget:
  max_cost_usd: 0.75
tasks:
  - id: a
    prompt: x
  - id: b
    depends_on: [a]
    prompt: x
  - id: c
    depends_on: [b]
    prompt: x
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	var buf bytes.Buffer
	_, err := Run(context.Background(), newTestObserver(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home})

	var got *executor.BudgetExceededError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As BudgetExceededError failed; err = %v\noutput:\n%s", err, buf.String())
	}
}
