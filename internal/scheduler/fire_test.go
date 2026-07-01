package scheduler

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/store"
)

type daemonEchoRuntime struct{}

func (daemonEchoRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	if req.Model != "m1" {
		return fmt.Errorf("model %q: %w", req.Model, runtime.ErrUnsupportedModel)
	}
	return nil
}

func (daemonEchoRuntime) Run(context.Context, runtime.Request) (runtime.Response, error) {
	return runtime.Response{Output: "ok"}, nil
}

// requiredParamShellWorkflow declares a required param with no default, so a
// fire that supplies no params fails at resolution before any task runs.
const requiredParamShellWorkflow = `
name: shellwf
params:
  - name: env
    required: true
tasks:
  - id: a
    command: echo hi
`

const unresolvedRuntimeWorkflow = `
name: shellwf
runtime: "{{params.rt}}"
model: m1
params:
  - name: rt
tasks:
  - id: a
    prompt: hello
`

// TestDaemonExecuteUnloadableWorkflowRecordsNoRun pins execute's load-error
// branch: when a due schedule's workflow file no longer parses, the fire
// surfaces the load error, records no run, and leaves LastRunID empty rather
// than stamping a phantom run id onto the schedule.
func TestDaemonExecuteUnloadableWorkflowRecordsNoRun(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, "name: shellwf\ntasks: [unterminated")

	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    true,
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := New(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")

	results := make(chan fireResult, 1)
	d.scan(false, results)

	res := awaitResult(t, results)
	if res.err == nil {
		t.Fatal("fire result err = nil, want a workflow load error")
	}
	if res.runID != "" {
		t.Fatalf("runID = %q, want empty (load failed before any run started)", res.runID)
	}
	d.complete(res)

	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (load failed)", len(runs))
	}
	got, err := schedule.Get(home, added.ID)
	if err != nil {
		t.Fatalf("Get after failed fire: %v", err)
	}
	if got.LastRunID != "" {
		t.Fatalf("LastRunID = %q, want empty (no run was recorded)", got.LastRunID)
	}
}

// TestDaemonExecuteMissingRequiredParamRecordsNoRun pins execute's param-
// resolve-error branch: a due schedule whose workflow declares a required param
// the record never supplies fails at resolution, records no run, and leaves
// LastRunID empty.
func TestDaemonExecuteMissingRequiredParamRecordsNoRun(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, requiredParamShellWorkflow)

	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    true,
		// Params omitted: the required `env` param is never supplied.
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := New(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")

	results := make(chan fireResult, 1)
	d.scan(false, results)

	res := awaitResult(t, results)
	if res.err == nil {
		t.Fatal("fire result err = nil, want a param-resolution error")
	}
	if res.runID != "" {
		t.Fatalf("runID = %q, want empty (param resolution failed before any run started)", res.runID)
	}
	d.complete(res)

	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (param resolution failed)", len(runs))
	}
	got, err := schedule.Get(home, added.ID)
	if err != nil {
		t.Fatalf("Get after failed fire: %v", err)
	}
	if got.LastRunID != "" {
		t.Fatalf("LastRunID = %q, want empty (no run was recorded)", got.LastRunID)
	}
}

// TestDaemonExecuteInvalidRoutingRecordsNoRun pins execute's routing-validation
// branch: a due schedule whose workflow lacks runtime routing fails before any
// run starts, records no run, and leaves LastRunID empty.
func TestDaemonExecuteInvalidRoutingRecordsNoRun(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, unresolvedRuntimeWorkflow)

	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "shellwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    true,
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := New(home, "", runtime.Default(), io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")

	results := make(chan fireResult, 1)
	d.scan(false, results)

	res := awaitResult(t, results)
	if res.err == nil {
		t.Fatal("fire result err = nil, want a routing-validation error")
	}
	if !strings.Contains(res.err.Error(), "validate routing") {
		t.Fatalf("err = %q, want validate routing context", res.err.Error())
	}
	if res.runID != "" {
		t.Fatalf("runID = %q, want empty (routing validation failed before any run started)", res.runID)
	}
	d.complete(res)

	if runs, _ := store.ListRuns(home, "shellwf"); len(runs) != 0 {
		t.Fatalf("got %d runs, want 0 (routing validation failed)", len(runs))
	}
	got, err := schedule.Get(home, added.ID)
	if err != nil {
		t.Fatalf("Get after failed fire: %v", err)
	}
	if got.LastRunID != "" {
		t.Fatalf("LastRunID = %q, want empty (no run was recorded)", got.LastRunID)
	}
}

func TestDaemonExecuteUsesExplicitCatalog(t *testing.T) {
	home := t.TempDir()
	path := writeWorkflow(t, `
name: llmwf
runtime: sched-catalog-only
model: m1
tasks:
  - id: a
    prompt: hello
`)
	if _, ok := runtime.Default().Resolve("sched-catalog-only"); ok {
		t.Fatal("sched-catalog-only unexpectedly present in default registry")
	}
	reg := &runtime.Registry{}
	reg.Register("sched-catalog-only", daemonEchoRuntime{})

	added, err := schedule.Add(home, schedule.Record{
		WorkflowID: "llmwf",
		Ref:        path,
		Path:       path,
		Trigger:    schedule.Trigger{Cron: "* * * * *", TZ: "UTC"},
		Enabled:    true,
	}, schedule.Config{Now: fixedClock("2026-06-28T10:00:30Z"), Rand: counterRand(1)})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := New(home, "", reg, io.Discard)
	d.now = fixedClock("2026-06-28T10:01:05Z")

	results := make(chan fireResult, 1)
	d.scan(false, results)

	res := awaitResult(t, results)
	if res.err != nil {
		t.Fatalf("fire error: %v", res.err)
	}
	d.complete(res)

	runs, err := store.ListRuns(home, "llmwf")
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	got, err := schedule.Get(home, added.ID)
	if err != nil {
		t.Fatalf("Get after fire: %v", err)
	}
	if got.LastRunID == "" {
		t.Fatal("LastRunID empty, want recorded run id")
	}
}
