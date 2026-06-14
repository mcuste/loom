// Package workflow defines the domain types for loom workflow definitions.
//
// A Workflow is the parsed, validated representation of a YAML workflow file.
// It carries a list of Tasks; each Task is a Prompt sent to a model. Runtime,
// Model, Effort, and the runtime registry live in the runtime package; this
// package imports them only to type the workflow- and task-level defaults.
package workflow

import (
	"fmt"
	"regexp"

	"github.com/mcuste/loom/pkg/runtime"
)

// WorkflowID is a validated workflow identifier: non-empty, [A-Za-z0-9_]+.
type WorkflowID string

// TaskID is a validated task identifier: non-empty, [A-Za-z0-9_]+.
type TaskID string

// ParamName is a validated workflow-parameter identifier: non-empty,
// [A-Za-z0-9_]+. It shares the alphabet with WorkflowID and TaskID so the
// parser can validate `{{params.x}}` placeholders with the same identifier
// regex used for task placeholders.
type ParamName string

// identifierClass is the character class that defines a valid WorkflowID,
// TaskID, or ParamName, and — by extension — the alphabet for `{{id}}` and
// `{{params.name}}` placeholder names recognized by the parser. identifierRe,
// placeholderRe, paramPlaceholderRe, and combinedPlaceholderRe all derive
// from this constant so the regexes cannot drift apart.
const identifierClass = `[A-Za-z0-9_]+`

var (
	identifierRe = regexp.MustCompile(`^` + identifierClass + `$`)

	// placeholderRe matches `{{id}}` placeholders in a prompt. The captured
	// name must satisfy identifierClass, the same alphabet as a TaskID, so a
	// placeholder can never reference a name that could never be a valid id.
	placeholderRe = regexp.MustCompile(`\{\{(` + identifierClass + `)\}\}`)

	// paramPlaceholderRe matches `{{params.name}}` placeholders. The captured
	// name must satisfy identifierClass, the same alphabet as a ParamName.
	paramPlaceholderRe = regexp.MustCompile(`\{\{params\.(` + identifierClass + `)\}\}`)

	// combinedPlaceholderRe matches BOTH `{{params.name}}` and `{{id}}` in a
	// single pass. Capture group 1 is the param name (non-empty for a param
	// match); capture group 2 is the task id (non-empty for a task match).
	// Used by Substitute to splice both kinds of placeholder in one pass so a
	// param value containing `{{taskid}}` text is never re-expanded.
	combinedPlaceholderRe = regexp.MustCompile(`\{\{(?:params\.(` + identifierClass + `)|(` + identifierClass + `))\}\}`)
)

// NewWorkflowID validates s and returns it as a WorkflowID.
//
// Returns an error if s is empty or contains a character outside [A-Za-z0-9_].
func NewWorkflowID(s string) (WorkflowID, error) {
	if !identifierRe.MatchString(s) {
		return "", &InvalidWorkflowIDError{Value: s}
	}
	return WorkflowID(s), nil
}

// InvalidWorkflowIDError reports a WorkflowID that failed the `[A-Za-z0-9_]+` rule.
type InvalidWorkflowIDError struct {
	Value string
}

func (e *InvalidWorkflowIDError) Error() string {
	return fmt.Sprintf("invalid workflow id %q: must match [A-Za-z0-9_]+", e.Value)
}

// NewTaskID validates s and returns it as a TaskID.
//
// Returns an error if s is empty or contains a character outside [A-Za-z0-9_].
func NewTaskID(s string) (TaskID, error) {
	if !identifierRe.MatchString(s) {
		return "", &InvalidTaskIDError{Value: s}
	}
	return TaskID(s), nil
}

// InvalidTaskIDError reports a TaskID that failed the `[A-Za-z0-9_]+` rule.
type InvalidTaskIDError struct {
	Value string
}

func (e *InvalidTaskIDError) Error() string {
	return fmt.Sprintf("invalid task id %q: must match [A-Za-z0-9_]+", e.Value)
}

// NewParamName validates s and returns it as a ParamName.
//
// Returns an error if s is empty or contains a character outside [A-Za-z0-9_].
func NewParamName(s string) (ParamName, error) {
	if !identifierRe.MatchString(s) {
		return "", &InvalidParamNameError{Value: s}
	}
	return ParamName(s), nil
}

// InvalidParamNameError reports a ParamName that failed the `[A-Za-z0-9_]+` rule.
type InvalidParamNameError struct {
	Value string
}

func (e *InvalidParamNameError) Error() string {
	return fmt.Sprintf("invalid param name %q: must match [A-Za-z0-9_]+", e.Value)
}

// Workflow is the validated, in-memory representation of a workflow YAML file.
type Workflow struct {
	// ID uniquely identifies the workflow.
	ID WorkflowID
	// Description is shown in plan output; not sent to the model.
	Description string
	// Runtime is the default runtime inherited by tasks that do not specify
	// their own.
	Runtime runtime.Name
	// Model is the default model inherited by tasks that do not specify their own.
	Model runtime.Model
	// Effort is the default effort inherited by tasks that do not specify their own.
	Effort runtime.Effort
	// SystemPrompt is appended to the runtime's system prompt for every task.
	// Each task's effective runtime must support a system prompt when this is
	// non-empty; otherwise validation fails with runtime.ErrUnsupportedSystemPrompt.
	SystemPrompt string
	// Params declares the workflow's CLI-passable parameters in declaration
	// order. Resolved values substitute into prompts via `{{params.name}}`.
	Params []Param
	// Tasks are the workflow's tasks in declaration order.
	Tasks []Task

	// byID maps TaskID → index into Tasks for O(1) lookup. Populated by Parse;
	// nil for hand-constructed Workflow values, in which case ByID falls back
	// to a linear scan.
	byID map[TaskID]int
	// paramByName maps ParamName → index into Params for O(1) lookup. Populated
	// by Parse; nil for hand-constructed Workflow values, in which case Param
	// falls back to a linear scan.
	paramByName map[ParamName]int
}

// Task is a single executable unit within a Workflow.
type Task struct {
	// ID uniquely identifies the task within its workflow.
	ID TaskID
	// Prompt is the text sent to the model, with `{{id}}` placeholders to be
	// substituted by upstream task outputs at run time. Empty for shell tasks;
	// see Command.
	Prompt string
	// Command is the shell command body executed via `sh -c`. Mutually
	// exclusive with Prompt. Placeholders `{{id}}` and `{{params.x}}` are
	// substituted before execution.
	Command string
	// Description is shown in plan output; not sent to the model.
	Description string
	// Runtime overrides Workflow.Runtime for this task when non-empty.
	Runtime runtime.Name
	// Model overrides Workflow.Model for this task when non-empty.
	Model runtime.Model
	// Effort overrides Workflow.Effort for this task when non-empty.
	Effort runtime.Effort
	// DependsOn names the tasks this task depends on. Populated from explicit
	// `depends_on` in YAML and auto-extended at parse for every `{{id}}`
	// placeholder found in the prompt.
	DependsOn []TaskID
}

// IsShell reports whether t is a shell task (has Command set) rather than an
// LLM task. The parser enforces XOR between Prompt and Command, so this is a
// reliable discriminator at the executor, CLI, and store layers.
func (t Task) IsShell() bool { return t.Command != "" }

// Param is a declared workflow parameter — a named value supplied at run time
// via `-p key=val` (or a defaults block) and substituted into prompts via
// `{{params.name}}`.
type Param struct {
	// Name uniquely identifies the parameter within its workflow.
	Name ParamName
	// Description is shown in plan output; not sent to the model.
	Description string
	// Default is the value used when no CLI/file value is supplied. Stored as
	// the YAML scalar's literal text. HasDefault distinguishes "" (the empty
	// string was the declared default) from "no default declared".
	Default string
	// HasDefault is true when the YAML declared a `default:` key, including
	// `default: ""`. Mutually exclusive with Required.
	HasDefault bool
	// Required marks a parameter that must be supplied at run time.
	Required bool
}

// Param returns a pointer to the Param with the given name, or nil if none
// match. Parallels ByID for Tasks; falls back to a linear scan for
// hand-constructed Workflow values where the index was not populated.
func (w *Workflow) Param(name ParamName) *Param {
	if w.paramByName != nil {
		i, ok := w.paramByName[name]
		if !ok {
			return nil
		}
		return &w.Params[i]
	}
	for i := range w.Params {
		if w.Params[i].Name == name {
			return &w.Params[i]
		}
	}
	return nil
}
