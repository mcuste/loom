package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/tui"
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
	home := loomHomeForTest(t)
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
	if err := runWorkflow(tui.New(&buf), &buf, runRequest{wf: wf, manifest: []byte(manifest), resolved: resolved, home: home}); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "Seeded   :") {
		t.Errorf("plain run emitted a Seeded line:\n%s", out)
	}
	if !strings.Contains(out, `✓ workflow "wf" complete`) {
		t.Errorf("plain run did not run to completion:\n%s", out)
	}
}

// TestRunWorkflow_SeededRunPrintsSeededCount pins that a non-empty seedPlan
// prints the "Seeded   : N task(s) from prior run" line with the seed count.
func TestRunWorkflow_SeededRunPrintsSeededCount(t *testing.T) {
	home := loomHomeForTest(t)
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
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(tui.New(&buf), &buf, runRequest{wf: wf, manifest: []byte(manifest), resolved: resolved, home: home, plan: plan}); err != nil {
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
	home := loomHomeForTest(t)
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
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(tui.New(&buf), &buf, runRequest{wf: wf, manifest: []byte(manifest), resolved: resolved, home: home, plan: plan}); err != nil {
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
	home := loomHomeForTest(t)
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
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(tui.New(&buf), &buf, runRequest{wf: wf, manifest: []byte(manifest), resolved: resolved, home: home, plan: plan}); err != nil {
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
	home := loomHomeForTest(t)
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
		entries: []seedEntry{{id: "a", prompt: "would-fail-if-rerun", output: "stored-a"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(tui.New(&buf), &buf, runRequest{wf: wf, manifest: []byte(manifest), resolved: resolved, home: home, plan: plan}); err != nil {
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
	home := loomHomeForTest(t)
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
		entries: []seedEntry{{id: "ghost", prompt: "p", output: "stored-ghost"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(tui.New(&buf), &buf, runRequest{wf: wf, manifest: []byte(manifest), resolved: resolved, home: home, plan: plan}); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
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

	rec := readNewRun(t, "wf", "")
	tasks, _ := rec["tasks"].([]any)
	for _, raw := range tasks {
		if entry, ok := raw.(map[string]any); ok && entry["id"] == "ghost" {
			t.Errorf("ghost seed was stamped into the run record but its id is absent from the workflow")
		}
	}
}

// TestRunCommand_RecordsInvocationCwd pins that a run records the directory it
// was invoked from in the run record's `cwd` field, so a later resume can
// restore it.
func TestRunCommand_RecordsInvocationCwd(t *testing.T) {
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	path := writeWorkflow(t, `
name: wf
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello
`)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", path})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, buf.String())
	}

	data, err := os.ReadFile(filepath.Join(testRunsDir(t), "wf", "latest.json"))
	if err != nil {
		t.Fatalf("read latest.json: %v", err)
	}
	var record struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal run record: %v\nraw:\n%s", err, data)
	}
	if record.Cwd != cwd {
		t.Errorf("record.cwd = %q, want %q (invocation cwd not recorded)", record.Cwd, cwd)
	}
}

// TestRunCommand_PersistsExecutorOutputThroughStoreHooks pins the cmd/loom
// pass-through: storeHooks now wires run.OnFinish straight onto
// executor.Hooks.OnFinish with no field-by-field copy, so the executor's
// TaskResult.Output must reach the on-disk record verbatim. The cmd-echo fake
// echoes the substituted prompt as its output, so a correct pass-through
// writes that text into tasks[0].output.
func TestRunCommand_PersistsExecutorOutputThroughStoreHooks(t *testing.T) {
	path := writeWorkflow(t, `
name: wf
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello world
`)
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", path})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, buf.String())
	}

	data, err := os.ReadFile(filepath.Join(testRunsDir(t), "wf", "latest.json"))
	if err != nil {
		t.Fatalf("read latest.json: %v", err)
	}
	var record struct {
		Tasks []struct {
			Output string `json:"output"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal run record: %v\nraw:\n%s", err, data)
	}
	if len(record.Tasks) != 1 {
		t.Fatalf("len(record.tasks) = %d, want 1", len(record.Tasks))
	}
	if got := record.Tasks[0].Output; got != "hello world" {
		t.Fatalf("tasks[0].output = %q, want %q", got, "hello world")
	}
}

// cmdCostRuntime is a no-binary fake registered for the budget surfacing test.
// Each Run succeeds and reports a fixed cost so a chained workflow accumulates
// a predictable TotalCostUSD and trips the workflow budget.
type cmdCostRuntime struct{}

func (cmdCostRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdCostRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{TotalCostUSD: 0.5},
	}, nil
}

func init() {
	runtime.Register("cmd-cost", cmdCostRuntime{})
}

// TestRunWorkflow_SurfacesBudgetExceeded pins that when the executor aborts on
// the workflow cost budget, runWorkflow surfaces the typed
// executor.BudgetExceededError to the caller rather than swallowing it. The
// three-task chain at cost 0.5 each overruns the 0.75 budget before its last
// task is dispatched.
func TestRunWorkflow_SurfacesBudgetExceeded(t *testing.T) {
	home := loomHomeForTest(t)
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
	err := runWorkflow(tui.New(&buf), &buf, runRequest{wf: wf, manifest: []byte(manifest), resolved: resolved, home: home})

	var got *executor.BudgetExceededError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As BudgetExceededError failed; err = %v\noutput:\n%s", err, buf.String())
	}
}
