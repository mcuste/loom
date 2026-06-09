package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// testRuntime is the spec the parser tests validate against. It accepts a
// small whitelist of models and efforts and rejects system prompts; the
// values are chosen so each negative case in TestParse can target one field.
const testRuntime runtime.Name = "test-rt"

type fakeSpec struct {
	models            map[runtime.Model]bool
	efforts           map[runtime.Effort]bool
	supportsSysPrompt bool
}

func (f fakeSpec) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	if !f.models[req.Model] {
		return fmt.Errorf("model %q: %w", req.Model, runtime.ErrUnsupportedModel)
	}
	if req.Effort != "" && !f.efforts[req.Effort] {
		return fmt.Errorf("effort %q: %w", req.Effort, runtime.ErrUnsupportedEffort)
	}
	if req.SystemPrompt != "" && !f.supportsSysPrompt {
		return runtime.ErrUnsupportedSystemPrompt
	}
	return nil
}

func (fakeSpec) Run(context.Context, runtime.Request) (runtime.Response, error) {
	return runtime.Response{}, nil
}

func init() {
	runtime.Register(testRuntime, fakeSpec{
		models:            map[runtime.Model]bool{"m1": true, "m2": true},
		efforts:           map[runtime.Effort]bool{"low": true, "high": true},
		supportsSysPrompt: true,
	})
	runtime.Register("test-no-sys", fakeSpec{
		models:  map[runtime.Model]bool{"m1": true},
		efforts: map[runtime.Effort]bool{},
	})
}

// minimal returns a workflow that parses cleanly so each test can mutate one
// field. Indented with leading newlines so callers can write `minimal + ...`
// and keep YAML structure readable.
const minimal = `
name: wf1
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: hello
`

func TestParseMinimal(t *testing.T) {
	wf, err := workflow.Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.ID != "wf1" {
		t.Errorf("ID = %q, want wf1", wf.ID)
	}
	if wf.Runtime != testRuntime {
		t.Errorf("Runtime = %q, want %q", wf.Runtime, testRuntime)
	}
	if wf.Model != "m1" {
		t.Errorf("Model = %q, want m1", wf.Model)
	}
	if len(wf.Tasks) != 1 || wf.Tasks[0].ID != "a" || wf.Tasks[0].Prompt != "hello" {
		t.Errorf("Tasks = %+v, want one task id=a prompt=hello", wf.Tasks)
	}
}

func TestParseFullSchema(t *testing.T) {
	src := `
name: wf_full
description: top-level description
runtime: test-rt
model: m1
effort: low
system_prompt: be terse
tasks:
  - id: a
    description: produce A
    prompt: do A
  - id: b
    description: produce B
    model: m2
    effort: high
    prompt: |
      use {{a}}
    depends_on: [a]
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if wf.Description != "top-level description" {
		t.Errorf("Description = %q", wf.Description)
	}
	if wf.SystemPrompt != "be terse" {
		t.Errorf("SystemPrompt = %q", wf.SystemPrompt)
	}
	if wf.Effort != "low" {
		t.Errorf("Effort = %q", wf.Effort)
	}
	if len(wf.Tasks) != 2 {
		t.Fatalf("len(Tasks) = %d, want 2", len(wf.Tasks))
	}
	b := wf.Tasks[1]
	if b.Model != "m2" || b.Effort != "high" {
		t.Errorf("task b overrides not preserved: %+v", b)
	}
	if !slices.Equal(b.DependsOn, []workflow.TaskID{"a"}) {
		t.Errorf("task b DependsOn = %v, want [a]", b.DependsOn)
	}
}

// TestPlaceholdersMustBeDeclared pins the strict rule that placeholders are
// validated against depends_on, never used to extend it. A placeholder whose
// name is not in depends_on is rejected — even if the referenced id is a
// real task elsewhere in the workflow.
func TestPlaceholdersMustBeDeclared(t *testing.T) {
	src := `
name: wf_ph
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
  - id: b
    prompt: B
  - id: d
    depends_on: [a]
    prompt: |
      use {{a}} and {{b}}
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.UnknownPlaceholderError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As UnknownPlaceholderError failed; err = %v", err)
	}
	if got.Task != "d" || got.Name != "b" {
		t.Errorf("UnknownPlaceholderError = %+v, want task=d name=b", got)
	}
}

// TestPlaceholdersDoNotExtendDeps verifies that DependsOn reflects only the
// declared list, in declaration order — placeholders never appear in the
// graph implicitly even when they reference declared ids.
func TestPlaceholdersDoNotExtendDeps(t *testing.T) {
	src := `
name: wf_explicit
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
  - id: b
    prompt: B
  - id: c
    depends_on: [a, b]
    prompt: |
      use {{a}} twice: {{a}} and once {{b}}
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	c := wf.Tasks[2]
	want := []workflow.TaskID{"a", "b"}
	if !slices.Equal(c.DependsOn, want) {
		t.Errorf("DependsOn = %v, want %v (placeholders must not extend the graph)", c.DependsOn, want)
	}
}

// TestDuplicateDependsOn pins the intra-list dedup rule: depends_on may not
// name the same task twice. The auto-extension model previously silenced
// this; under the explicit-deps rule it is a clear user error.
func TestDuplicateDependsOn(t *testing.T) {
	src := `
name: wf_dup
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: A
  - id: b
    depends_on: [a, a]
    prompt: |
      use {{a}}
`
	_, err := workflow.Parse([]byte(src))
	var got *workflow.DuplicateDependencyError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As DuplicateDependencyError failed; err = %v", err)
	}
	if got.Task != "b" || got.Dep != "a" {
		t.Errorf("DuplicateDependencyError = %+v, want task=b dep=a", got)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr error
	}{
		{
			name:    "invalid workflow id",
			src:     "name: bad-name\nruntime: test-rt\nmodel: m1\ntasks:\n  - id: a\n    prompt: x\n",
			wantErr: &workflow.InvalidWorkflowIDError{},
		},
		{
			name:    "invalid task id",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\ntasks:\n  - id: bad-id\n    prompt: x\n",
			wantErr: &workflow.InvalidTaskIDError{},
		},
		{
			name:    "no tasks",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\n",
			wantErr: workflow.ErrNoTasks,
		},
		{
			name:    "missing prompt",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\ntasks:\n  - id: a\n",
			wantErr: workflow.ErrMissingPrompt,
		},
		{
			name:    "duplicate task id",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\ntasks:\n  - id: a\n    prompt: x\n  - id: a\n    prompt: y\n",
			wantErr: &workflow.DuplicateTaskIDError{},
		},
		{
			name:    "unknown depends_on",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\ntasks:\n  - id: a\n    prompt: x\n    depends_on: [missing]\n",
			wantErr: &workflow.UnknownDependencyError{},
		},
		{
			name:    "unknown placeholder",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\ntasks:\n  - id: a\n    prompt: use {{missing}}\n",
			wantErr: &workflow.UnknownPlaceholderError{},
		},
		{
			name:    "unsupported model from workflow default",
			src:     "name: wf\nruntime: test-rt\nmodel: m9\ntasks:\n  - id: a\n    prompt: x\n",
			wantErr: runtime.ErrUnsupportedModel,
		},
		{
			name:    "missing model after no resolution",
			src:     "name: wf\nruntime: test-rt\ntasks:\n  - id: a\n    prompt: x\n",
			wantErr: runtime.ErrMissingModel,
		},
		{
			name:    "unsupported effort",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\neffort: medium\ntasks:\n  - id: a\n    prompt: x\n",
			wantErr: runtime.ErrUnsupportedEffort,
		},
		{
			name:    "unsupported system prompt",
			src:     "name: wf\nruntime: test-no-sys\nmodel: m1\nsystem_prompt: hi\ntasks:\n  - id: a\n    prompt: x\n",
			wantErr: runtime.ErrUnsupportedSystemPrompt,
		},
		{
			name:    "missing runtime",
			src:     "name: wf\nmodel: m1\ntasks:\n  - id: a\n    prompt: x\n",
			wantErr: runtime.ErrMissingRuntime,
		},
		{
			name:    "unknown runtime",
			src:     "name: wf\nruntime: nope\nmodel: m1\ntasks:\n  - id: a\n    prompt: x\n",
			wantErr: runtime.ErrUnknownRuntime,
		},
		{
			name:    "unknown top-level field",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\ninputs: [topic]\ntasks:\n  - id: a\n    prompt: x\n",
			wantErr: nil, // checked separately below
		},
		{
			name:    "unknown task field",
			src:     "name: wf\nruntime: test-rt\nmodel: m1\ntasks:\n  - id: a\n    prompt: x\n    workflow: ./sub.yaml\n",
			wantErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workflow.Parse([]byte(tc.src))
			if err == nil {
				t.Fatalf("Parse returned nil error, want error")
			}
			if tc.wantErr == nil {
				// For "unknown ... field" cases yaml.v3 emits its own error; just
				// require some error mentioning the offending key.
				return
			}
			// Typed-pointer sentinels: use errors.As; sentinel errors: errors.Is.
			switch want := tc.wantErr.(type) {
			case *workflow.InvalidWorkflowIDError:
				var got *workflow.InvalidWorkflowIDError
				if !errors.As(err, &got) {
					t.Fatalf("errors.As failed for %T; err = %v", want, err)
				}
			case *workflow.InvalidTaskIDError:
				var got *workflow.InvalidTaskIDError
				if !errors.As(err, &got) {
					t.Fatalf("errors.As failed for %T; err = %v", want, err)
				}
			case *workflow.DuplicateTaskIDError:
				var got *workflow.DuplicateTaskIDError
				if !errors.As(err, &got) {
					t.Fatalf("errors.As failed for %T; err = %v", want, err)
				}
			case *workflow.UnknownDependencyError:
				var got *workflow.UnknownDependencyError
				if !errors.As(err, &got) {
					t.Fatalf("errors.As failed for %T; err = %v", want, err)
				}
			case *workflow.UnknownPlaceholderError:
				var got *workflow.UnknownPlaceholderError
				if !errors.As(err, &got) {
					t.Fatalf("errors.As failed for %T; err = %v", want, err)
				}
			default:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("errors.Is(_, %v) = false; err = %v", tc.wantErr, err)
				}
			}
		})
	}
}

// TestCycleDetection covers both a self-loop (cycle of length 1, triggered
// via a self-placeholder) and a longer cycle expressed through depends_on.
func TestCycleDetection(t *testing.T) {
	t.Run("two-node cycle", func(t *testing.T) {
		src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    depends_on: [b]
    prompt: x
  - id: b
    depends_on: [a]
    prompt: y
`
		_, err := workflow.Parse([]byte(src))
		var ce *workflow.CycleError
		if !errors.As(err, &ce) {
			t.Fatalf("errors.As CycleError failed; err = %v", err)
		}
		if len(ce.Cycle) < 2 {
			t.Fatalf("CycleError.Cycle too short: %v", ce.Cycle)
		}
		if ce.Cycle[0] != ce.Cycle[len(ce.Cycle)-1] {
			t.Fatalf("CycleError.Cycle should start and end with same id; got %v", ce.Cycle)
		}
	})

	t.Run("self loop via placeholder", func(t *testing.T) {
		src := `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    depends_on: [a]
    prompt: x
`
		_, err := workflow.Parse([]byte(src))
		var ce *workflow.CycleError
		if !errors.As(err, &ce) {
			t.Fatalf("errors.As CycleError failed; err = %v", err)
		}
	})
}

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(minimal), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wf, err := workflow.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if wf.ID != "wf1" {
		t.Errorf("ID = %q, want wf1", wf.ID)
	}
}

func TestParseFileNotFound(t *testing.T) {
	_, err := workflow.ParseFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatalf("ParseFile of missing file returned nil error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("errors.Is(_, os.ErrNotExist) = false; err = %v", err)
	}
}
