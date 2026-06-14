package codex_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/runtime/codex"
)

func TestRegisteredUnderExpectedName(t *testing.T) {
	if codex.Name != "codex" {
		t.Fatalf("Name = %q, want %q", codex.Name, "codex")
	}
	if _, ok := runtime.Lookup(codex.Name); !ok {
		t.Fatalf("runtime %q not registered", codex.Name)
	}
}

func TestSatisfiesSubprocess(t *testing.T) {
	r, ok := runtime.Lookup(codex.Name)
	if !ok {
		t.Fatalf("runtime %q not registered", codex.Name)
	}
	sub, ok := r.(runtime.Subprocess)
	if !ok {
		t.Fatalf("registered runner does not implement runtime.Subprocess")
	}
	if got := sub.Binary(); got != "codex" {
		t.Fatalf("Binary() = %q, want %q", got, "codex")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     runtime.Request
		wantErr error
	}{
		{"gpt-5.5 no effort", runtime.Request{Model: "gpt-5.5"}, nil},
		{"gpt-5.4 low", runtime.Request{Model: "gpt-5.4", Effort: "low"}, nil},
		{"gpt-5.4-mini medium", runtime.Request{Model: "gpt-5.4-mini", Effort: "medium"}, nil},
		{"gpt-5.3-codex-spark high", runtime.Request{Model: "gpt-5.3-codex-spark", Effort: "high"}, nil},
		{"gpt-5.5 minimal", runtime.Request{Model: "gpt-5.5", Effort: "minimal"}, nil},
		{"gpt-5.4 xhigh", runtime.Request{Model: "gpt-5.4", Effort: "xhigh"}, nil},
		{"gpt-5.4-mini no effort", runtime.Request{Model: "gpt-5.4-mini"}, nil},
		{"gpt-5.3-codex-spark no effort", runtime.Request{Model: "gpt-5.3-codex-spark"}, nil},

		{"missing model", runtime.Request{}, runtime.ErrMissingModel},
		{"unsupported model", runtime.Request{Model: "sonnet"}, runtime.ErrUnsupportedModel},
		{"unsupported model gpt-5.2", runtime.Request{Model: "gpt-5.2"}, runtime.ErrUnsupportedModel},
		{"unsupported model gpt-5.3-codex", runtime.Request{Model: "gpt-5.3-codex"}, runtime.ErrUnsupportedModel},
		{"unsupported effort", runtime.Request{Model: "gpt-5.5", Effort: "extreme"}, runtime.ErrUnsupportedEffort},
		{"unsupported effort none", runtime.Request{Model: "gpt-5.5", Effort: "none"}, runtime.ErrUnsupportedEffort},
		{"system prompt rejected", runtime.Request{Model: "gpt-5.5", SystemPrompt: "be terse"}, runtime.ErrUnsupportedSystemPrompt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runtime.Validate(codex.Name, tc.req)
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
