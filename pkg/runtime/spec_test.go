package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

func modelSet(ms ...runtime.Model) map[runtime.Model]struct{} {
	s := make(map[runtime.Model]struct{}, len(ms))
	for _, m := range ms {
		s[m] = struct{}{}
	}
	return s
}

func effortSet(es ...runtime.Effort) map[runtime.Effort]struct{} {
	s := make(map[runtime.Effort]struct{}, len(es))
	for _, e := range es {
		s[e] = struct{}{}
	}
	return s
}

// TestSpecBinary_ReturnsConfiguredName pins that the Binary method surfaces the
// Spec's configured executable name (and that the Subprocess contract is met by
// the method, not the field).
func TestSpecBinary_ReturnsConfiguredName(t *testing.T) {
	s := runtime.Spec{Name: "claude-code", BinaryName: "claude"}
	if got := s.Binary(); got != "claude" {
		t.Fatalf("Binary() = %q, want %q", got, "claude")
	}
}

// TestSpecValidate covers the shared routing checks the Spec lifts out of the
// adapters: model required, model/effort membership, and the system-prompt
// accept/reject policy. Each rejection must wrap the matching sentinel.
func TestSpecValidate(t *testing.T) {
	accepting := runtime.Spec{
		Name:               "fake-accept",
		Models:             modelSet("m1", "m2"),
		Efforts:            effortSet("low", "high"),
		AcceptSystemPrompt: true,
	}
	rejecting := runtime.Spec{
		Name:               "fake-reject",
		Models:             modelSet("m1"),
		AcceptSystemPrompt: false,
	}

	tests := []struct {
		name    string
		spec    runtime.Spec
		req     runtime.Request
		wantErr error // nil for success
	}{
		{"ok no effort no sys", accepting, runtime.Request{Model: "m1"}, nil},
		{"ok effort and sys", accepting, runtime.Request{Model: "m2", Effort: "high", SystemPrompt: "be terse"}, nil},
		{"ok no effort with sys", rejecting, runtime.Request{Model: "m1"}, nil},

		{"missing model", accepting, runtime.Request{}, runtime.ErrMissingModel},
		{"unsupported model", accepting, runtime.Request{Model: "m9"}, runtime.ErrUnsupportedModel},
		{"unsupported effort", accepting, runtime.Request{Model: "m1", Effort: "medium"}, runtime.ErrUnsupportedEffort},
		{"system prompt rejected", rejecting, runtime.Request{Model: "m1", SystemPrompt: "be terse"}, runtime.ErrUnsupportedSystemPrompt},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.spec.Validate(tc.req)
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

// TestSpecRun_DecodesStdout proves the shared Run builds argv from Args(req),
// execs the binary, and hands captured stdout to Decode whose Response is
// returned verbatim. The sh script echoes the request prompt back so a correct
// Args->argv->stdout->Decode chain yields the prompt as output.
func TestSpecRun_DecodesStdout(t *testing.T) {
	var captured string
	s := runtime.Spec{
		Name:       "fake",
		BinaryName: "sh",
		Args: func(req runtime.Request) []string {
			return []string{"-c", `printf %s "$0"`, req.Prompt}
		},
		Decode: func(stdout []byte) (runtime.Response, error) {
			captured = string(stdout)
			return runtime.Response{Output: string(stdout)}, nil
		},
	}

	resp, err := s.Run(context.Background(), runtime.Request{Prompt: "PAYLOAD-123"})
	if err != nil {
		t.Fatalf("Run returned error %v, want nil", err)
	}
	if resp.Output != "PAYLOAD-123" {
		t.Fatalf("Run output = %q, want %q (captured stdout = %q)", resp.Output, "PAYLOAD-123", captured)
	}
}

// TestSpecRun_HonorsContextCancellation proves Run threads ctx into
// exec.CommandContext: a context cancelled before Run kills the subprocess
// instead of blocking on it, and the failure surfaces as an *ExecError. The sh
// script would otherwise sleep well beyond the test's patience.
func TestSpecRun_HonorsContextCancellation(t *testing.T) {
	s := runtime.Spec{
		Name:       "fake",
		BinaryName: "sh",
		Args: func(runtime.Request) []string {
			return []string{"-c", "sleep 30"}
		},
		Decode: func([]byte) (runtime.Response, error) {
			t.Fatal("Decode must not run when the context is cancelled")
			return runtime.Response{}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Run(ctx, runtime.Request{})
	var ee *runtime.ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("Run error = %v, want errors.As(_, *runtime.ExecError)", err)
	}
	if ee.Err == nil {
		t.Fatalf("ExecError.Err = nil, want the wrapped exec error")
	}
}

// TestSpecRun_WrapsExecErrorOnFailure proves a non-zero exit is reported as a
// *runtime.ExecError carrying the Spec Name and captured stderr, and that Decode
// is never reached on failure.
func TestSpecRun_WrapsExecErrorOnFailure(t *testing.T) {
	s := runtime.Spec{
		Name:       "fake",
		BinaryName: "sh",
		Args: func(runtime.Request) []string {
			return []string{"-c", "echo boom 1>&2; exit 7"}
		},
		Decode: func([]byte) (runtime.Response, error) {
			t.Fatal("Decode must not run when the subprocess fails")
			return runtime.Response{}, nil
		},
	}

	_, err := s.Run(context.Background(), runtime.Request{})
	var ee *runtime.ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("Run error = %v, want errors.As(_, *runtime.ExecError)", err)
	}
	if ee.Name != "fake" {
		t.Fatalf("ExecError.Name = %q, want %q", ee.Name, "fake")
	}
	if ee.Err == nil {
		t.Fatalf("ExecError.Err = nil, want the wrapped exec error")
	}
	if !strings.Contains(ee.Stderr, "boom") {
		t.Fatalf("ExecError.Stderr = %q, want it to contain %q", ee.Stderr, "boom")
	}
	if ee.ExitCode != 7 {
		t.Fatalf("ExecError.ExitCode = %d, want 7", ee.ExitCode)
	}
}

// TestSpecRun_ExitCodeZeroOnSuccess pins that a clean run reports ExitCode 0 on
// the Response, the success-side counterpart to ExecError.ExitCode.
func TestSpecRun_ExitCodeZeroOnSuccess(t *testing.T) {
	s := runtime.Spec{
		Name:       "fake",
		BinaryName: "sh",
		Args:       func(runtime.Request) []string { return []string{"-c", "echo ok"} },
		Decode:     func(b []byte) (runtime.Response, error) { return runtime.Response{Output: string(b)}, nil },
	}
	resp, err := s.Run(context.Background(), runtime.Request{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("Response.ExitCode = %d, want 0", resp.ExitCode)
	}
}

// TestSpecRun_PropagatesDecodeError proves a Decode failure surfaces as a
// non-ExecError error that unwraps to the Decode sentinel and carries the
// runtime name prefix (the parse-json prefix lifted out of the adapters).
func TestSpecRun_PropagatesDecodeError(t *testing.T) {
	sentinel := errors.New("decode failed")
	s := runtime.Spec{
		Name:       "fake",
		BinaryName: "sh",
		Args: func(runtime.Request) []string {
			return []string{"-c", "printf ignored"}
		},
		Decode: func([]byte) (runtime.Response, error) {
			return runtime.Response{}, sentinel
		},
	}

	_, err := s.Run(context.Background(), runtime.Request{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Run error = %v, want errors.Is(_, sentinel)", err)
	}
	var ee *runtime.ExecError
	if errors.As(err, &ee) {
		t.Fatalf("Run error = %v, want a decode error, not an *ExecError", err)
	}
	if !strings.Contains(err.Error(), "fake") {
		t.Fatalf("Run error = %q, want it to carry the runtime name prefix %q", err.Error(), "fake")
	}
}
