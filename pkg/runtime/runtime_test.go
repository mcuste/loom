package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

// fakeSpec is a hand-rolled Runner used across runtime tests. Construct with
// newFake; whitelisted models/efforts and SystemPrompt support are captured
// by value. Run is a no-op stub; the registry tests never invoke it.
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

func newFake(models []runtime.Model, efforts []runtime.Effort, sys bool) fakeSpec {
	f := fakeSpec{
		models:            map[runtime.Model]bool{},
		efforts:           map[runtime.Effort]bool{},
		supportsSysPrompt: sys,
	}
	for _, m := range models {
		f.models[m] = true
	}
	for _, e := range efforts {
		f.efforts[e] = true
	}
	return f
}

// TestRunnerValidate exercises the Runner.Validate contract directly on a
// fake runner, with no registry involvement. Dispatch-level errors (missing
// or unknown runtime) and runtime-name wrapping live in registry_test.go.
func TestRunnerValidate(t *testing.T) {
	full := newFake([]runtime.Model{"m1", "m2"}, []runtime.Effort{"low", "high"}, true)
	noSys := newFake([]runtime.Model{"m1"}, nil, false)

	tests := []struct {
		name    string
		runner  runtime.Runner
		req     runtime.Request
		wantErr error // nil for success
	}{
		{"ok no effort no sys", full, runtime.Request{Model: "m1"}, nil},
		{"ok with effort and sys", full, runtime.Request{Model: "m2", Effort: "high", SystemPrompt: "you are helpful"}, nil},

		{"missing model", full, runtime.Request{}, runtime.ErrMissingModel},
		{"unsupported model", full, runtime.Request{Model: "m3"}, runtime.ErrUnsupportedModel},
		{"unsupported effort", full, runtime.Request{Model: "m1", Effort: "medium"}, runtime.ErrUnsupportedEffort},
		{"unsupported system prompt", noSys, runtime.Request{Model: "m1", SystemPrompt: "sys"}, runtime.ErrUnsupportedSystemPrompt},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.runner.Validate(tc.req)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate returned %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate returned %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}
