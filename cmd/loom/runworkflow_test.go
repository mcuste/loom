package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// parseAndResolve is a test helper that parses a manifest and resolves its
// params with the given CLI map and lower-precedence file/record tier, so the
// runWorkflow tests can hand the unified pipeline exactly what doRun and
// runFromRecord would.
func parseAndResolve(t *testing.T, manifest string, cli, lower map[string]string) (*workflow.Workflow, workflow.ParamValues) {
	t.Helper()
	wf, err := workflow.Parse([]byte(manifest))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	resolved, err := workflow.ResolveParams(wf, cli, lower)
	if err != nil {
		t.Fatalf("resolve params: %v", err)
	}
	return wf, resolved
}

// TestRunWorkflow_PlainRunCompletesWithoutSeededLine pins that calling the
// unified pipeline with a zero seedPlan runs every task and prints the plain
// summary, with no "Seeded" line anywhere in the output. This guards the
// byte-identity of a plain `loom run` after the de-duplication.
func TestRunWorkflow_PlainRunCompletesWithoutSeededLine(t *testing.T) {
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
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, seedPlan{}); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "Seeded") {
		t.Errorf("plain run emitted a Seeded line:\n%s", out)
	}
	if !strings.Contains(out, `✓ workflow "wf" complete`) {
		t.Errorf("plain run did not run to completion:\n%s", out)
	}
}

// TestRunWorkflow_SeededRunPrintsSeededCount pins that a non-empty seedPlan
// prints the "Seeded   : N task(s) from prior run" line with the seed count.
func TestRunWorkflow_SeededRunPrintsSeededCount(t *testing.T) {
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

	plan := seedPlan{
		seed:    map[workflow.TaskID]string{"a": "stored-a"},
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, plan); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
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

	plan := seedPlan{
		seed:    map[workflow.TaskID]string{"a": "stored-a"},
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, plan); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}

	rec := readNewRun(t, "wf", "")
	tasks, _ := rec["tasks"].([]any)
	var bPrompt string
	for _, raw := range tasks {
		entry := raw.(map[string]any)
		if entry["id"] == "b" {
			bPrompt, _ = entry["prompt"].(string)
		}
	}
	if bPrompt != "got: stored-a" {
		t.Errorf("b.prompt = %q, want %q (seed of a did not bypass runtime and feed downstream)", bPrompt, "got: stored-a")
	}
}

// TestRunWorkflow_SeededRunStampsSeedIntoRecord pins that each seeded entry is
// stamped into the fresh run record as an already-ok task before the executor
// starts, so a later resume of this run finds it complete rather than
// re-dispatching it.
func TestRunWorkflow_SeededRunStampsSeedIntoRecord(t *testing.T) {
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

	plan := seedPlan{
		seed:    map[workflow.TaskID]string{"a": "stored-a"},
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, plan); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}

	rec := readNewRun(t, "wf", "")
	tasks, _ := rec["tasks"].([]any)
	var aOutput string
	var sawA bool
	for _, raw := range tasks {
		entry := raw.(map[string]any)
		if entry["id"] == "a" {
			sawA = true
			aOutput, _ = entry["output"].(string)
		}
	}
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

	plan := seedPlan{
		seed:    map[workflow.TaskID]string{"a": "stored-a"},
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, plan); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
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
	chdirTo(t, t.TempDir())

	manifest := `name: wf
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello world
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	plan := seedPlan{
		seed:    map[workflow.TaskID]string{"ghost": "stored-ghost"},
		entries: []seedEntry{{id: "ghost", prompt: "p", output: "stored-ghost"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, plan); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "Seeded") {
		t.Errorf("out-of-workflow seed should not print a Seeded line:\n%s", out)
	}
	if !strings.Contains(out, "[1/1]") {
		t.Errorf("ghost seed must not reduce expected count below the real task count; got:\n%s", out)
	}
	if !strings.Contains(out, `✓ workflow "wf" complete`) {
		t.Errorf("run did not complete:\n%s", out)
	}

	rec := readNewRun(t, "wf", "")
	tasks, _ := rec["tasks"].([]any)
	for _, raw := range tasks {
		if entry, ok := raw.(map[string]any); ok && entry["id"] == "ghost" {
			t.Errorf("ghost seed was stamped into the run record but its id is absent from the workflow")
		}
	}
}
