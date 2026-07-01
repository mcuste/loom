package workflowcheck_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/mcuste/loom/pkg/workflowcheck"
)

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

func testCatalog() *runtime.Registry {
	reg := &runtime.Registry{}
	reg.Register("test-rt", fakeSpec{
		models:            map[runtime.Model]bool{"m1": true, "m2": true},
		efforts:           map[runtime.Effort]bool{"low": true, "high": true},
		supportsSysPrompt: true,
	})
	reg.Register("test-no-sys", fakeSpec{
		models:  map[runtime.Model]bool{"m1": true},
		efforts: map[runtime.Effort]bool{},
	})
	return reg
}

func TestValidateUsesExplicitCatalog(t *testing.T) {
	reg := &runtime.Registry{}
	reg.Register("catalog-only", fakeSpec{
		models:            map[runtime.Model]bool{"m1": true},
		efforts:           map[runtime.Effort]bool{},
		supportsSysPrompt: true,
	})
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: "catalog-only",
		Model:   "m1",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "hello"},
		},
	}

	params, err := workflow.ResolveParams(wf, nil, nil)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if err := workflowcheck.Validate(wf, params, reg, false); err != nil {
		t.Fatalf("Validate with explicit catalog: %v", err)
	}
	if err := workflowcheck.Validate(wf, params, runtime.Default(), false); !errors.Is(err, runtime.ErrUnknownRuntime) {
		t.Fatalf("Validate with default registry err = %v, want ErrUnknownRuntime", err)
	}
}

func TestValidateRoutingTable(t *testing.T) {
	reg := testCatalog()
	tests := []struct {
		name    string
		src     string
		wantErr error
	}{
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wf, err := workflow.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			params, err := workflow.ResolveParams(wf, nil, nil)
			if err != nil {
				t.Fatalf("ResolveParams: %v", err)
			}
			err = workflowcheck.Validate(wf, params, reg, false)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateResolvesParamFields(t *testing.T) {
	src := `
name: wf
runtime: test-rt
model: "{{params.model}}"
effort: "{{params.effort}}"
params:
  - name: model
    default: m2
  - name: effort
    default: high
tasks:
  - id: a
    prompt: x
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	params, err := workflow.ResolveParams(wf, nil, nil)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if err := workflowcheck.Validate(wf, params, testCatalog(), false); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateTaskSystemPromptUnsupported(t *testing.T) {
	src := `
name: wf
runtime: test-no-sys
model: m1
tasks:
  - id: a
    system_prompt: override
    prompt: go
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	params, err := workflow.ResolveParams(wf, nil, nil)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if err := workflowcheck.Validate(wf, params, testCatalog(), true); !errors.Is(err, runtime.ErrUnsupportedSystemPrompt) {
		t.Fatalf("Validate err = %v, want ErrUnsupportedSystemPrompt", err)
	}
}

func TestValidateTaskSystemPromptAcceptedViaRuntimeOverride(t *testing.T) {
	src := `
name: wf
runtime: test-no-sys
model: m1
tasks:
  - id: a
    runtime: test-rt
    system_prompt: override
    prompt: go
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	params, err := workflow.ResolveParams(wf, nil, nil)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if err := workflowcheck.Validate(wf, params, testCatalog(), true); err != nil {
		t.Fatalf("Validate rejected runtime override: %v", err)
	}
}

func TestResolveAndValidateParams(t *testing.T) {
	wf := &workflow.Workflow{
		ID:      "wf",
		Runtime: "test-rt",
		Model:   "m1",
		Params: []workflow.Param{
			{Name: "env", Default: "dev", HasDefault: true},
		},
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "hello"},
		},
	}

	got, err := workflowcheck.ResolveAndValidateParams(wf, nil, nil, testCatalog())
	if err != nil {
		t.Fatalf("ResolveAndValidateParams: %v", err)
	}
	if got["env"] != "dev" {
		t.Fatalf("got[env] = %q, want dev", got["env"])
	}
}

func TestResolveAndValidateParamsRejectsBadRouting(t *testing.T) {
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "a", Prompt: "hello"},
		},
	}

	_, err := workflowcheck.ResolveAndValidateParams(wf, nil, nil, testCatalog())
	if !errors.Is(err, runtime.ErrMissingRuntime) {
		t.Fatalf("err = %v, want missing runtime", err)
	}
}
