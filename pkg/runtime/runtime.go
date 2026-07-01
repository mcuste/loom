// Package runtime defines the contract between the workflow layer and
// pluggable runtimes that actually execute tasks against an LLM.
//
// Runner is the single contract every registered runtime satisfies: it both
// validates a Request's routing fields (Validate) and executes a task (Run).
// Capability sub-interfaces (Subprocess, API) describe transport-specific
// preflight needs (binary on PATH, env vars set) without affecting the
// core contract.
//
// Runtimes register themselves with the package registry at init time;
// user-defined runtimes can be registered the same way once loom configs
// add an extension point.
//
// Validation errors wrap one of the package's sentinel errors (ErrMissing*,
// ErrUnsupported*); callers test with errors.Is.
package runtime

import (
	"context"
	"errors"
)

// Name identifies a runtime in workflow YAML, e.g. "claude-code".
type Name string

// Model identifies the model a runtime should use for a task. The value is
// opaque to this package; each runtime interprets it (e.g. "sonnet" for
// claude-code, "gpt-5" for openai-api, "llama3.1:70b" for ollama). Validity
// is checked per runtime via Runner.Validate.
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
// runtime's contract. Callers that build a Request outside the YAML parser
// path should call Validate before Run; runtimes assume their input has been
// validated and do not re-check.
type Request struct {
	// TaskID is the workflow.TaskID this Request was built for, as a plain
	// string. Typed as string (not workflow.TaskID) because the workflow
	// package imports this one: promoting it to workflow.TaskID here would
	// create an import cycle. Runtimes treat it as an opaque identifier for
	// telemetry and error context.
	TaskID       string
	Prompt       string
	Model        Model
	Effort       Effort
	SystemPrompt string
	// WorkingDir is the directory the runtime's child process runs in. Empty
	// means inherit loom's process cwd. The executor fills it from the
	// workflow's resolved working_dir.
	WorkingDir string
}

// Runner is the contract every registered runtime satisfies. It validates a
// Request's routing fields and executes the task end-to-end.
//
// Validate reports whether a Request's routing fields (Model, Effort,
// SystemPrompt) are accepted, returning one of the package's sentinel errors
// (wrapped with field context) on rejection. Prompt and TaskID are not part
// of the accept/reject contract.
type Runner interface {
	Validate(Request) error
	Run(ctx context.Context, req Request) (Response, error)
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
	CacheReadTokens int     // not surfaced by every runtime
	TotalCostUSD    float64 // not surfaced by every runtime
}

// Add folds other into the receiver, accumulating a running total across task
// executions.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.TotalCostUSD += other.TotalCostUSD
}

// Response is the output of a single task execution.
type Response struct {
	Output string
	Usage  Usage
	// ExitCode is the runtime binary's process exit code: 0 on a clean run, the
	// child's code on a non-zero exit, and -1 when the process was killed by a
	// signal or never started. The executor records it on the task result and, for
	// a task whose ok_exit tolerates the code, treats the run as a success whose
	// code is readable downstream via `{{id.exit}}`.
	ExitCode int
}

// Subprocess is implemented by runtimes that shell out to a local binary
// (claude-code, codex, gemini-cli). Binary returns the executable name
// expected on $PATH so the executor can preflight-check availability.
type Subprocess interface {
	Runner
	Binary() string
}
