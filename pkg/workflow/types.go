// Package workflow defines the domain types for loom workflow definitions.
//
// A Workflow is the parsed, validated representation of a YAML workflow file.
// It carries a list of Tasks; each Task is either a Prompt sent to a model or
// a SubWorkflow reference to another loaded Workflow.
package workflow

import (
	"fmt"
	"regexp"
)

// Runtime names what runs a task: a CLI tool (e.g. claude-code, codex,
// gemini-cli) or an API client (e.g. claude-api, openai-api, alibaba-api,
// ollama). Each runtime registers a RuntimeSpec with the package-level
// registry; see registry.go.
type Runtime string

// Model identifies the model a runtime should use for a task. The value is
// opaque to the workflow package; each runtime interprets it (e.g. "sonnet"
// for claude-code, "gpt-5" for openai-api, "llama3.1:70b" for ollama).
// Validity is checked per runtime via RuntimeSpec.Accepts.
type Model string

// Effort hints at the reasoning effort a runtime should apply to a task. The
// value is opaque to the workflow package; each runtime interprets it (e.g.
// "low"/"medium"/"high" for openai-api, an integer token budget like "8000"
// for claude-api, empty to leave the runtime default in place). Validity is
// checked per runtime via RuntimeSpec.AcceptsEffort.
type Effort string

// TaskID is a validated task identifier: non-empty, [A-Za-z0-9_]+.
//
// The `__` digraph is reserved as the namespace separator used by
// sub-workflow flattening and is rejected by NewTaskID.
type TaskID string

var taskIDRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// NewTaskID validates s and returns it as a TaskID.
//
// Returns an error if s is empty, contains a character outside [A-Za-z0-9_],
// or contains the reserved `__` digraph.
func NewTaskID(s string) (TaskID, error) {
	if !taskIDRe.MatchString(s) {
		return "", &InvalidTaskIDError{Value: s}
	}
	if containsDoubleUnderscore(s) {
		return "", &ReservedSeparatorError{Value: s}
	}
	return TaskID(s), nil
}

func containsDoubleUnderscore(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '_' && s[i+1] == '_' {
			return true
		}
	}
	return false
}

// TaskOrigin records where a flattened task originated when its source file
// differs from the root workflow.
type TaskOrigin struct {
	// File is the canonical path of the workflow YAML that declared the task.
	File string
	// OriginalID is the task's id as it appeared in its source workflow,
	// before any `__`-namespacing applied during flattening.
	OriginalID TaskID
}

// Workflow is the validated, in-memory representation of a workflow YAML file.
type Workflow struct {
	// Name is the human-readable workflow name.
	Name string
	// Runtime is the default runtime inherited by tasks that do not specify
	// their own.
	Runtime Runtime
	// Model is the default model inherited by tasks that do not specify their own.
	Model Model
	// Effort is the default effort inherited by tasks that do not specify their own.
	Effort Effort
	// SystemPrompt is prepended to every task's context when set.
	SystemPrompt string
	// Inputs are the names this workflow declares; bound by a parent step
	// via Task.With when used as a sub-workflow. Empty for root workflows.
	Inputs []string
	// Output names the task whose result is exported when this workflow is
	// used as a sub-workflow. Empty for root workflows.
	Output TaskID
	// SourceFile is the canonical filesystem path of the YAML file this
	// workflow was parsed from; used to resolve sub-workflow paths.
	SourceFile string
	// Tasks are the workflow's tasks in declaration order.
	Tasks []Task
}

// TaskKind discriminates the work a Task performs.
//
// Implementations: PromptKind, SubWorkflowKind.
type TaskKind interface {
	isTaskKind()
}

// PromptKind is a prompt-driven task sent to a model.
type PromptKind struct {
	// Prompt is the text sent to the model, with `{{id}}` placeholders to be
	// substituted by upstream task outputs at run time.
	Prompt string
}

func (PromptKind) isTaskKind() {}

// SubWorkflowKind is a reference to another workflow, loaded eagerly at parse.
type SubWorkflowKind struct {
	// Path is the canonicalized filesystem path of the child workflow YAML.
	Path string
	// With maps each of the child's declared input names to a value string
	// supplied by the parent step (literal or `{{placeholder}}`).
	With map[string]string
	// Loaded is the fully parsed child workflow, ready for flattening.
	Loaded *Workflow
}

func (SubWorkflowKind) isTaskKind() {}

// Task is a single executable unit within a Workflow.
type Task struct {
	// ID uniquely identifies the task within its workflow.
	ID TaskID
	// Kind is the work this task performs.
	Kind TaskKind
	// Description is shown in plan output; not sent to the model.
	Description string
	// Runtime overrides Workflow.Runtime for this task when non-empty.
	Runtime Runtime
	// Model overrides Workflow.Model for this task when non-empty.
	Model Model
	// Effort overrides Workflow.Effort for this task when non-empty.
	Effort Effort
	// DependsOn names the tasks this task depends on. Populated from explicit
	// `depends_on` in YAML and auto-extended at parse for every `{{id}}`
	// placeholder found in the prompt.
	DependsOn []TaskID
}

// InvalidTaskIDError reports a TaskID that failed the `[A-Za-z0-9_]+` rule.
type InvalidTaskIDError struct {
	Value string
}

func (e *InvalidTaskIDError) Error() string {
	return fmt.Sprintf("invalid task id %q: must match [A-Za-z0-9_]+", e.Value)
}

// ReservedSeparatorError reports a TaskID containing the reserved `__` digraph.
type ReservedSeparatorError struct {
	Value string
}

func (e *ReservedSeparatorError) Error() string {
	return fmt.Sprintf("task id %q contains reserved separator \"__\"", e.Value)
}
