package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ExecError is returned when a Subprocess runtime's binary fails. It carries
// the runtime Name (used to derive the message prefix), the wrapped exec error,
// and the verbatim stderr so callers (and errors.Is / errors.As) can inspect
// each separately instead of digging through a formatted string.
type ExecError struct {
	Name   Name
	Err    error
	Stderr string
}

// Error renders "<name>: <err>" or "<name>: <err>: <trimmed stderr>".
func (e *ExecError) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		return fmt.Sprintf("%s: %s", e.Name, e.Err)
	}
	return fmt.Sprintf("%s: %s: %s", e.Name, e.Err, stderr)
}

// Unwrap exposes the wrapped exec error for errors.Is / errors.As.
func (e *ExecError) Unwrap() error { return e.Err }

// Spec captures the per-runtime variation behind the shared Subprocess
// scaffolding: identity, the binary to exec, the accepted Model/Effort sets,
// the system-prompt policy, the argv builder, and the stdout decoder. A Spec
// value satisfies runtime.Subprocess via the shared Validate, Run, and Binary
// methods below.
type Spec struct {
	Name               Name
	BinaryName         string
	Models             map[Model]struct{}
	Efforts            map[Effort]struct{}
	AcceptSystemPrompt bool
	// Args maps an already-validated Request to the subprocess argv. Must be
	// non-nil before Run is called; the Request it receives has passed Validate,
	// so Args need not re-check routing fields.
	Args func(Request) []string
	// Decode maps captured stdout to a Response. Must be non-nil before Run is
	// called.
	Decode func(stdout []byte) (Response, error)
}

// Compile-time proof that Spec satisfies the Subprocess contract.
var _ Subprocess = Spec{}

// Validate applies the shared routing checks (model required, model in the
// allowed set, effort in the allowed set, system-prompt policy). Rejections
// wrap the matching sentinel; the runtime-name prefix is added by the package
// Validate dispatcher.
func (s Spec) Validate(req Request) error {
	if req.Model == "" {
		return ErrMissingModel
	}
	if _, ok := s.Models[req.Model]; !ok {
		return fmt.Errorf("model %q: %w", req.Model, ErrUnsupportedModel)
	}
	if req.Effort != "" {
		if _, ok := s.Efforts[req.Effort]; !ok {
			return fmt.Errorf("effort %q: %w", req.Effort, ErrUnsupportedEffort)
		}
	}
	if req.SystemPrompt != "" && !s.AcceptSystemPrompt {
		return ErrUnsupportedSystemPrompt
	}
	return nil
}

// Binary returns the executable name expected on $PATH.
func (s Spec) Binary() string { return s.BinaryName }

// Run execs the binary with Args(req), captures stdout/stderr, wraps a failed
// run in *ExecError, and hands stdout to Decode on success. A Decode failure
// surfaces as a name-prefixed error (not an *ExecError).
func (s Spec) Run(ctx context.Context, req Request) (Response, error) {
	if s.Args == nil || s.Decode == nil {
		return Response{}, fmt.Errorf("%s: incomplete Spec: Args and Decode must be non-nil", s.Name)
	}
	cmd := exec.CommandContext(ctx, s.BinaryName, s.Args(req)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Response{}, &ExecError{Name: s.Name, Err: err, Stderr: stderr.String()}
	}

	resp, err := s.Decode(stdout.Bytes())
	if err != nil {
		return Response{}, fmt.Errorf("%s: %w", s.Name, err)
	}
	return resp, nil
}
