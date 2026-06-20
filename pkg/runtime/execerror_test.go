package runtime_test

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

// TestExecError_FormatsWithStderr pins the "<name>: <err>: <stderr>" shape and
// that the stderr tail is whitespace-trimmed.
func TestExecError_FormatsWithStderr(t *testing.T) {
	e := &runtime.ExecError{
		Name:   "codex",
		Err:    errors.New("exit status 1"),
		Stderr: "boom\n",
	}
	want := "codex: exit status 1: boom"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestExecError_FormatsWithoutStderr pins the "<name>: <err>" shape when stderr
// is empty, with no trailing separator.
func TestExecError_FormatsWithoutStderr(t *testing.T) {
	e := &runtime.ExecError{
		Name: "claude-code",
		Err:  errors.New("exit status 2"),
	}
	want := "claude-code: exit status 2"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestExecError_DerivesPrefixFromName proves the message prefix comes from Name
// rather than a hard-coded runtime string.
func TestExecError_DerivesPrefixFromName(t *testing.T) {
	e := &runtime.ExecError{
		Name: "gemini-cli",
		Err:  errors.New("boom"),
	}
	want := "gemini-cli: boom"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestExecError_TrimsSurroundingStderrWhitespace pins that leading and trailing
// whitespace around the captured stderr is stripped before formatting.
func TestExecError_TrimsSurroundingStderrWhitespace(t *testing.T) {
	e := &runtime.ExecError{
		Name:   "codex",
		Err:    errors.New("exit status 1"),
		Stderr: "  \n panic: nope \n\n",
	}
	want := "codex: exit status 1: panic: nope"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestExecError_UnwrapsWrappedError proves errors.Is reaches the wrapped exec
// error through ExecError.Unwrap.
func TestExecError_UnwrapsWrappedError(t *testing.T) {
	inner := errors.New("inner exec failure")
	e := &runtime.ExecError{Name: "codex", Err: inner}
	if !errors.Is(e, inner) {
		t.Fatalf("errors.Is(ExecError, inner) = false, want true")
	}
}
