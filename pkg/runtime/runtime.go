// Package runtime defines the contract between the workflow layer and
// pluggable runtimes that actually execute tasks against an LLM.
//
// Two concerns live here:
//
//   - The validation contract (RuntimeSpec.Validate) used by the workflow
//     parser to accept a YAML manifest. Runtimes register themselves with
//     the package registry at init time; user-defined runtimes can be
//     registered the same way once loom configs add an extension point.
//
//   - The execution contract (Runtime, with capability sub-interfaces
//     SubprocessRuntime and APIRuntime). Executors call Run; capability
//     interfaces support preflight checks (binary on PATH, env vars set).
//
// Validation errors wrap one of the package's sentinel errors (ErrMissing*,
// ErrUnsupported*); callers test with errors.Is.
package runtime

import (
	"context"
	"errors"
	"fmt"
)

// Name identifies a runtime in workflow YAML, e.g. "claude-code".
type Name string

// Model identifies the model a runtime should use for a task. The value is
// opaque to this package; each runtime interprets it (e.g. "sonnet" for
// claude-code, "gpt-5" for openai-api, "llama3.1:70b" for ollama). Validity
// is checked per runtime via RuntimeSpec.Validate.
type Model string

// Effort hints at the reasoning effort a runtime should apply to a task. The
// value is opaque to this package; each runtime interprets it (e.g.
// "low"/"medium"/"high" for openai-api, an integer token budget like "8000"
// for claude-api, empty to leave the runtime default in place).
type Effort string

// Request is the fully resolved input for a single task execution.
// Placeholders in the original prompt have already been substituted by the
// executor; the runtime sees only the final text.
//
// Routing fields (Model, Effort, SystemPrompt) must satisfy the target
// runtime's spec. Callers that build a Request outside the YAML parser path
// should call Validate before Run; runtimes assume their input has been
// validated and do not re-check.
type Request struct {
	TaskID       string
	Prompt       string
	Model        Model
	Effort       Effort
	SystemPrompt string
}

// RuntimeSpec is the validation surface every registered runtime exposes.
// Implementations report whether a Request's routing fields (Model, Effort,
// SystemPrompt) are accepted, returning one of the package's sentinel errors
// (wrapped with field context) on rejection. Prompt and TaskID are not part
// of the accept/reject contract.
type RuntimeSpec interface {
	Validate(Request) error
}

// Validate looks name up in the registry and dispatches to its spec. Use it
// from the workflow parser and from any caller building a Request outside
// the parser path. Per-spec errors are wrapped with the runtime name so the
// caller's error message reads "<name>: <field> <value>: <sentinel>".
func Validate(name Name, req Request) error {
	if name == "" {
		return ErrMissingRuntime
	}
	spec, ok := Lookup(name)
	if !ok {
		return fmt.Errorf("%q: %w", name, ErrUnknownRuntime)
	}
	if err := spec.Validate(req); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Sentinel errors. Validation failures wrap one of these so callers can use
// errors.Is to test the failure mode independent of the runtime that raised
// it.
var (
	ErrMissingRuntime          = errors.New("runtime required (set workflow- or task-level runtime)")
	ErrUnknownRuntime          = errors.New("unknown runtime")
	ErrMissingModel            = errors.New("model required")
	ErrUnsupportedModel        = errors.New("unsupported model")
	ErrUnsupportedEffort       = errors.New("unsupported effort")
	ErrUnsupportedSystemPrompt = errors.New("runtime does not support a system prompt")
)

// Usage records cost and token accounting reported by the runtime. Fields
// left zero by runtimes that do not surface them.
type Usage struct {
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int     // TODO: might not be supported by all runtimes
	TotalCostUSD    float64 // TODO: might not be supported by all runtimes
}

// Response is the output of a single task execution.
type Response struct {
	Output string
	Usage  Usage
}

// Runtime is the execution contract every registered runtime satisfies. It
// composes the validation surface (RuntimeSpec) with a Run method that
// executes one task and returns the model's output.
type Runtime interface {
	RuntimeSpec
	Run(ctx context.Context, req Request) (Response, error)
}

// SubprocessRuntime is implemented by runtimes that shell out to a local
// binary (claude-code, codex, gemini-cli). Binary returns the executable
// name expected on $PATH so the executor can preflight-check availability.
type SubprocessRuntime interface {
	Runtime
	Binary() string
}

// APIRuntime is implemented by runtimes that call a remote API (claude-api,
// openai-api, alibaba-api, ollama). EnvVars returns the names of environment
// variables that must be set (e.g. "ANTHROPIC_API_KEY") so the executor can
// preflight-check credentials.
type APIRuntime interface {
	Runtime
	EnvVars() []string
}
