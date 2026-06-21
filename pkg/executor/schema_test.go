package executor_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// scriptSeq keeps scriptRuntime registration names unique within a test binary
// run; the runtime registry has no deregister and panics on a duplicate name.
var scriptSeq atomic.Uint64

// scriptRuntime returns successive outputs from outs on each Run call (clamping
// to the last entry once exhausted) and counts calls so a test can assert how
// many attempts the executor made. A fresh value is registered per test, so its
// state is never shared across parallel tests.
type scriptRuntime struct {
	mu    sync.Mutex
	calls int
	outs  []string
}

func (r *scriptRuntime) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *scriptRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (r *scriptRuntime) Run(_ context.Context, _ runtime.Request) (runtime.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.calls
	r.calls++
	out := r.outs[len(r.outs)-1]
	if i < len(r.outs) {
		out = r.outs[i]
	}
	return runtime.Response{Output: out}, nil
}

// newScript registers a fresh scriptRuntime under a name unique to t and
// returns the runtime name (for the workflow's `runtime:`) and the stub.
func newScript(t *testing.T, outs ...string) (runtime.Name, *scriptRuntime) {
	t.Helper()
	r := &scriptRuntime{outs: outs}
	name := runtime.Name("schema-script-" + t.Name() + "-" + strconv.FormatUint(scriptSeq.Add(1), 10))
	runtime.Register(name, r)
	return name, r
}

// objectSchema is the shared fixture: an object that must carry a string "name".
func objectSchema() *workflow.Schema {
	return &workflow.Schema{
		Type:       "object",
		Required:   []string{"name"},
		Properties: map[string]workflow.Property{"name": {Type: "string"}},
	}
}

// TestRun_SchemaValidObjectPasses pins that an LLM task whose output parses as
// JSON and matches its schema completes with the output preserved verbatim and
// is dispatched exactly once.
func TestRun_SchemaValidObjectPasses(t *testing.T) {
	t.Parallel()
	const out = `{"name":"loom"}`
	rt, script := newScript(t, out)
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: rt,
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "go", Schema: objectSchema()},
		},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if rep.Outputs["a"] != out {
		t.Errorf("Outputs[a] = %q, want %q", rep.Outputs["a"], out)
	}
	if got := script.callCount(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

// TestRun_SchemaMissingRequiredFieldRetriesThenSucceeds pins that a task whose
// first output omits a required field is retried under its retry policy, and
// the run succeeds once a conforming output is produced.
func TestRun_SchemaMissingRequiredFieldRetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	const good = `{"name":"loom"}`
	rt, script := newScript(t, `{"count":3}`, good)
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: rt,
		Model:   "m1",
		Tasks: []workflow.Task{
			{
				ID:     "a",
				Prompt: "go",
				Schema: objectSchema(),
				Retry:  workflow.Retry{Max: 2, Backoff: workflow.BackoffNone},
			},
		},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: unexpected error after retry: %v", err)
	}
	if rep.Outputs["a"] != good {
		t.Errorf("Outputs[a] = %q, want %q", rep.Outputs["a"], good)
	}
	if got := script.callCount(); got != 2 {
		t.Errorf("attempts = %d, want 2 (1 initial + 1 retry)", got)
	}
}

// TestRun_SchemaNonJSONFailsWithSchemaError pins that a task whose output is not
// JSON fails with a typed *executor.SchemaError when no retry policy is set, and
// is dispatched exactly once.
func TestRun_SchemaNonJSONFailsWithSchemaError(t *testing.T) {
	t.Parallel()
	rt, script := newScript(t, "this is not json")
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: rt,
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "go", Schema: objectSchema()},
		},
	}
	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatalf("Run: expected error for non-JSON output, got nil")
	}
	var schemaErr *executor.SchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("error is %T, want *executor.SchemaError; err = %v", err, err)
	}
	if got := script.callCount(); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry policy)", got)
	}
}

// TestRun_SchemaPropertyTypeMismatchFails pins that output whose required field
// is present but of the wrong JSON type (a number where the schema declares a
// string) fails with a typed *executor.SchemaError, exercising the
// property-level jsonTypeMatches dispatch.
func TestRun_SchemaPropertyTypeMismatchFails(t *testing.T) {
	t.Parallel()
	rt, script := newScript(t, `{"name":42}`)
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: rt,
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "go", Schema: objectSchema()},
		},
	}
	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatalf("Run: expected error for property type mismatch, got nil")
	}
	var schemaErr *executor.SchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("error is %T, want *executor.SchemaError; err = %v", err, err)
	}
	if got := script.callCount(); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry policy)", got)
	}
}

// TestRun_SchemaTopLevelTypeMismatchFails pins that output of the wrong
// top-level kind (a JSON array where the schema declares type: object) fails
// with a typed *executor.SchemaError, exercising the array/object dispatch in
// jsonTypeMatches at the integration level.
func TestRun_SchemaTopLevelTypeMismatchFails(t *testing.T) {
	t.Parallel()
	rt, script := newScript(t, `[]`)
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: rt,
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "go", Schema: objectSchema()},
		},
	}
	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatalf("Run: expected error for top-level type mismatch, got nil")
	}
	var schemaErr *executor.SchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("error is %T, want *executor.SchemaError; err = %v", err, err)
	}
	if got := script.callCount(); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry policy)", got)
	}
}

// TestRun_SchemaRetryExhaustionFailsWithSchemaError pins that when every attempt
// produces schema-invalid output, the run exhausts its retry budget and the
// final error is a typed *executor.SchemaError, dispatched Max+1 times.
func TestRun_SchemaRetryExhaustionFailsWithSchemaError(t *testing.T) {
	t.Parallel()
	rt, script := newScript(t, `{"count":1}`, `{"count":2}`, `{"count":3}`)
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: rt,
		Model:   "m1",
		Tasks: []workflow.Task{
			{
				ID:     "a",
				Prompt: "go",
				Schema: objectSchema(),
				Retry:  workflow.Retry{Max: 2, Backoff: workflow.BackoffNone},
			},
		},
	}
	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatalf("Run: expected error after retry exhaustion, got nil")
	}
	var schemaErr *executor.SchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("error is %T, want *executor.SchemaError; err = %v", err, err)
	}
	if got := script.callCount(); got != 3 {
		t.Errorf("attempts = %d, want 3 (1 initial + 2 retries)", got)
	}
}
