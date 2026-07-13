package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/run"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// testOutput reproduces the plain run lines the runner tests assert on
// without importing pkg/tui back into pkg/runner's test binary.
type testOutput struct {
	w     io.Writer
	total int
	step  int
	mu    sync.Mutex
}

func newTestOutput(w io.Writer) RunOutput { return &testOutput{w: w} }

func (o *testOutput) Header(meta RunMeta) error {
	o.total = meta.Total
	o.step = 0
	if _, err := fmt.Fprintf(o.w, "Run file : %s\nCwd      : %s\n\n", meta.RunFile, meta.Cwd); err != nil {
		return err
	}
	if meta.Seeded == 0 {
		return nil
	}
	_, err := fmt.Fprintf(o.w, "Seeded   : %d task(s) from prior run\n\n", meta.Seeded)
	return err
}

func (o *testOutput) Events() run.EventSink {
	return run.SinkFromHooks(executor.Hooks{
		OnStart: func(t workflow.Task, iter int, _ runtime.Name, _ runtime.Model, _ runtime.Effort) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.step++
			if iter >= 2 {
				o.total++
			}
			_, _ = fmt.Fprintf(o.w, "[%d/%d] %s\n", o.step, o.total, t.ID)
		},
		OnFinish: func(t workflow.Task, _ int, _ executor.TaskResult, err error) {
			o.mu.Lock()
			defer o.mu.Unlock()
			if err != nil {
				_, _ = fmt.Fprintf(o.w, "  %s FAIL: %v\n", t.ID, err)
				return
			}
			_, _ = fmt.Fprintf(o.w, "  %s done\n", t.ID)
		},
	})
}

func (o *testOutput) Summary(wf *workflow.Workflow, rep *executor.Report, expected int) error {
	done := len(distinctTaskIDs(rep))
	if done == expected {
		_, err := fmt.Fprintf(o.w, "✓ workflow %q complete\n", wf.ID)
		return err
	}
	_, err := fmt.Fprintf(o.w, "✗ workflow %q stopped after %d/%d tasks\n", wf.ID, done, expected)
	return err
}

func (o *testOutput) StoreError(err error) {
	_, _ = fmt.Fprintf(o.w, "  store: %v\n", err)
}

func distinctTaskIDs(rep *executor.Report) map[workflow.TaskID]struct{} {
	seen := make(map[workflow.TaskID]struct{}, len(rep.Tasks))
	for _, task := range rep.Tasks {
		seen[task.TaskID] = struct{}{}
	}
	return seen
}

// This file registers the no-binary fake runtimes the pkg/runner tests rely on.
// They mirror the fakes in cmd/loom/fakes_test.go; each lives in the package
// whose test suite needs them so neither pulls in the other's test binary.

// cmdEchoRuntime returns the substituted prompt verbatim so a test can confirm
// param substitution happened.
type cmdEchoRuntime struct{}

func (cmdEchoRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdEchoRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// cmdFailRuntime always errors on Run; seed/resume tests wire a task to it so
// that success proves the executor bypassed it entirely.
type cmdFailRuntime struct{}

func (cmdFailRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdFailRuntime) Run(_ context.Context, _ runtime.Request) (runtime.Response, error) {
	return runtime.Response{}, errors.New("cmd-fail must never be dispatched")
}

// cmdCostRuntime succeeds and reports a fixed cost so a chained workflow
// accumulates a predictable TotalCostUSD and trips the workflow budget.
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
	runtime.Register("cmd-echo", cmdEchoRuntime{})
	runtime.Register("cmd-fail", cmdFailRuntime{})
	runtime.Register("cmd-cost", cmdCostRuntime{})
}

// chdirTo cd's into dir for the rest of the test, restoring the original cwd
// via t.Cleanup.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// runnerTestHome creates a temp dir and returns it as the LOOM_HOME for a
// runner test. The runner receives home directly as Request.Home, so no env
// var is needed; the directory is created to mimic what loomHome() would do.
func runnerTestHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// testRunsDir returns the runs directory for a given home.
func testRunsDir(home string) string {
	return filepath.Join(home, "runs")
}

// readNewRun finds the run record file under <home>/runs/<wfID> whose name is
// not skipID. Used to inspect what a fresh invocation wrote without having to
// predict its (timestamp + random) run id.
func readNewRun(t *testing.T, home, wfID, skipID string) map[string]any {
	t.Helper()
	dir := filepath.Join(testRunsDir(home), wfID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var matches []string
	for _, e := range entries {
		name := e.Name()
		if name == "latest.json" || filepath.Ext(name) != ".json" {
			continue
		}
		if strings.TrimSuffix(name, ".json") == skipID {
			continue
		}
		matches = append(matches, name)
	}
	if len(matches) == 0 {
		t.Fatalf("no new run record produced under %s", dir)
	}
	if len(matches) > 1 {
		t.Fatalf("expected exactly one new run record under %s, found %d: %v", dir, len(matches), matches)
	}
	data, err := os.ReadFile(filepath.Join(dir, matches[0]))
	if err != nil {
		t.Fatalf("read new run: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal new run: %v", err)
	}
	return m
}

// taskField returns the string value of field for the task with the given id in
// a run record decoded as map[string]any, plus whether that task is present at
// all.
func taskField(t *testing.T, rec map[string]any, id, field string) (string, bool) {
	t.Helper()
	tasks, _ := rec["tasks"].([]any)
	for _, raw := range tasks {
		entry, ok := raw.(map[string]any)
		if !ok || entry["id"] != id {
			continue
		}
		val, _ := entry[field].(string)
		return val, true
	}
	return "", false
}

// parseAndResolve is a test helper that parses a manifest and resolves its
// params with the given CLI map and lower-precedence file/record tier.
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
	if _, err := Run(context.Background(), newTestOutput(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home}); err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "Seeded   :") {
		t.Errorf("plain run emitted a Seeded line:\n%s", out)
	}
	if !strings.Contains(out, `✓ workflow "wf" complete`) {
		t.Errorf("plain run did not run to completion:\n%s", out)
	}

	record, err := store.Load(filepath.Join(home, "runs", "wf", "latest.json"))
	if err != nil {
		t.Fatalf("load run record: %v", err)
	}
	if record.Trigger != store.TriggerCLI {
		t.Errorf("run trigger = %q, want %q", record.Trigger, store.TriggerCLI)
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
	if _, err := Run(context.Background(), newTestOutput(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
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
	if _, err := Run(context.Background(), newTestOutput(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
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
	if _, err := Run(context.Background(), newTestOutput(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
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
	if _, err := Run(context.Background(), newTestOutput(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
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
	if _, err := Run(context.Background(), newTestOutput(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home, Plan: plan}); err != nil {
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
	_, err := Run(context.Background(), newTestOutput(&buf), Request{Wf: wf, Manifest: []byte(manifest), Resolved: resolved, Home: home})

	var got *executor.BudgetExceededError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As BudgetExceededError failed; err = %v\noutput:\n%s", err, buf.String())
	}
}
