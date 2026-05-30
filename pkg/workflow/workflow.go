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

// identifierClass is the character class that defines a valid WorkflowID or
// TaskID, and — by extension — the alphabet for `{{id}}` placeholder names
// recognized by the parser. Both identifierRe and the parser's placeholderRe
// derive from this constant so the two regexes cannot drift apart.
const identifierClass = `[A-Za-z0-9_]+`

var identifierRe = regexp.MustCompile(`^` + identifierClass + `$`)

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
	// Tasks are the workflow's tasks in declaration order.
	Tasks []Task
}

// Task is a single executable unit within a Workflow.
type Task struct {
	// ID uniquely identifies the task within its workflow.
	ID TaskID
	// Prompt is the text sent to the model, with `{{id}}` placeholders to be
	// substituted by upstream task outputs at run time.
	Prompt string
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
