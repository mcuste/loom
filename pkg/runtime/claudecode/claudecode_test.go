package claudecode_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/runtime/claudecode"
)

func TestRegisteredUnderExpectedName(t *testing.T) {
	if claudecode.Name != "claude-code" {
		t.Fatalf("Name = %q, want %q", claudecode.Name, "claude-code")
	}
	if _, ok := runtime.Lookup(claudecode.Name); !ok {
		t.Fatalf("runtime %q not registered", claudecode.Name)
	}
}

func TestSatisfiesSubprocess(t *testing.T) {
	r, ok := runtime.Lookup(claudecode.Name)
	if !ok {
		t.Fatalf("runtime %q not registered", claudecode.Name)
	}
	sub, ok := r.(runtime.Subprocess)
	if !ok {
		t.Fatalf("registered runner does not implement runtime.Subprocess")
	}
	if got := sub.Binary(); got != "claude" {
		t.Fatalf("Binary() = %q, want %q", got, "claude")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     runtime.Request
		wantErr error // nil for success
	}{
		{"sonnet no effort", runtime.Request{Model: "sonnet"}, nil},
		{"opus high", runtime.Request{Model: "opus", Effort: "high"}, nil},
		{"haiku max with sys", runtime.Request{Model: "haiku", Effort: "max", SystemPrompt: "be terse"}, nil},

		{"missing model", runtime.Request{}, runtime.ErrMissingModel},
		{"unsupported model", runtime.Request{Model: "gpt-5"}, runtime.ErrUnsupportedModel},
		{"unsupported model lookalike", runtime.Request{Model: "sonnet-4-5"}, runtime.ErrUnsupportedModel},
		{"unsupported effort", runtime.Request{Model: "sonnet", Effort: "extreme"}, runtime.ErrUnsupportedEffort},
		{"unsupported effort numeric", runtime.Request{Model: "sonnet", Effort: "8000"}, runtime.ErrUnsupportedEffort},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runtime.Validate(claudecode.Name, tc.req)
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
