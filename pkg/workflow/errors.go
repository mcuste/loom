package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mcuste/loom/pkg/syntax"
)

// Sentinel parse errors. Typed errors below cover structured failures with
// fields the caller may want to inspect.
var (
	// ErrNoTasks is returned when the workflow YAML declares neither a top-level
	// task nor any loop, so there is nothing to run.
	ErrNoTasks = errors.New("workflow has no tasks")
	// ErrMissingParamName is returned when a params entry omits the name field.
	ErrMissingParamName = errors.New("param has no name")

	// ErrMissingPromptOrCommand reports a task that sets none of the body forms
	// (`prompt:`, `prompt_file:`, `command:`, `script:`, `loop:`, `for_each:`,
	// `for_each_parallel:`, or `workflow:`). Exactly one must be present.
	ErrMissingPromptOrCommand = errors.New("task has no prompt, prompt_file, command, script, loop, for_each, for_each_parallel, or workflow")
	// ErrPromptAndCommandSet reports a task that sets both `prompt:` and
	// `command:`. The two are mutually exclusive.
	ErrPromptAndCommandSet = errors.New("task sets both prompt and command")
	// ErrShellTaskWithSystemPrompt reports a shell task (one with `command:`) that
	// sets a task-level `system_prompt:` or `system_prompt_file:`. A shell task is
	// not sent to a model, so a system prompt is meaningless for it.
	ErrShellTaskWithSystemPrompt = errors.New("shell task must not set system_prompt or system_prompt_file")
	// ErrSubWorkflowWithSystemPrompt reports a sub-workflow task (one with
	// `workflow:`) that sets a task-level `system_prompt:` or `system_prompt_file:`.
	// The linked child carries its own system prompt; the wrapper has none.
	ErrSubWorkflowWithSystemPrompt = errors.New("sub-workflow task must not set system_prompt or system_prompt_file")
	// ErrShellTaskWithRuntime reports a shell task (one with `command:`) that
	// also sets task-level `runtime:`, `model:`, or `effort:`. These fields are
	// meaningless for shell tasks and rejected at the task level; workflow-level
	// defaults are tolerated.
	ErrShellTaskWithRuntime = errors.New("shell task must not set runtime, model, or effort")
	// ErrLoopTaskWithBody reports a loop-wrapper task (one with a `loop:` block)
	// that also sets a body form (`prompt:`, `prompt_file:`, or `command:`). A
	// loop task has no body of its own; its work lives in the nested loop tasks.
	ErrLoopTaskWithBody = errors.New("loop task must not set prompt, prompt_file, or command")
	// ErrLoopTaskWithRuntime reports a loop-wrapper task that sets task-level
	// `runtime:`, `model:`, or `effort:`. These belong to the loop's body tasks,
	// not the wrapper.
	ErrLoopTaskWithRuntime = errors.New("loop task must not set runtime, model, or effort")
	// ErrLoopTaskWithFields reports a loop-wrapper task that sets a task-only
	// field (`depends_on`, `when`, `writes_state`, `schema`, `retry`, `budget`,
	// `cache`, `ok_exit`, or `system_prompt`). The wrapper stands only for its
	// loop; entry dependencies and per-task behavior belong to the body tasks.
	ErrLoopTaskWithFields = errors.New("loop task must not set depends_on, when, writes_state, schema, retry, budget, cache, ok_exit, or system_prompt")
	// ErrLoopAndForEachSet reports a task declaring both a `loop:` and a
	// `for_each:` block. The two are sibling scoped-block forms; a task is at
	// most one of them.
	ErrLoopAndForEachSet = errors.New("task sets both loop and for_each")
	// ErrScriptTaskWithRuntime reports a script task (one with `script:`) that
	// also sets task-level `runtime:`, `model:`, or `effort:`. A script runs a
	// file directly and is not sent to a model, exactly like a shell task.
	ErrScriptTaskWithRuntime = errors.New("script task must not set runtime, model, or effort")
	// ErrScriptTaskWithSystemPrompt reports a script task (one with `script:`) that
	// sets a task-level `system_prompt:` or `system_prompt_file:`. A script is not
	// sent to a model, so a system prompt is meaningless for it.
	ErrScriptTaskWithSystemPrompt = errors.New("script task must not set system_prompt or system_prompt_file")
	// ErrScriptTaskWithSchema reports a script task (one with `script:`) that sets
	// a `schema:` block. Schema validation parses the output as JSON against the
	// model's structured response; a script's output is raw stdout, not validated.
	ErrScriptTaskWithSchema = errors.New("script task must not set schema")
	// ErrArgsWithoutScript reports a task that sets `args:` without a `script:`
	// body. argv is only meaningful for a script task.
	ErrArgsWithoutScript = errors.New("args is only valid on a script task")
	// ErrOkExitOnSubWorkflow reports a sub-workflow task (one with `workflow:`) that
	// sets `ok_exit`. A sub-workflow runs a child DAG, not a process, so it has no
	// exit code to tolerate.
	ErrOkExitOnSubWorkflow = errors.New("ok_exit is not valid on a sub-workflow task")
	// ErrOkExitOutOfRange reports an `ok_exit` entry outside the 0-255 Unix exit
	// code range. A negative or >255 value can never match a real process exit.
	ErrOkExitOutOfRange = errors.New("ok_exit code out of range (must be 0-255)")
)

// rejectLoopWrapperFields enforces that a `loop:` task carries nothing but its
// id, description, and the loop block: a loop wrapper is not an executable task,
// so prompt/command, runtime knobs, and every task-only field are rejected at
// load time rather than silently ignored.
func rejectLoopWrapperFields(tid TaskID, rt syntax.DraftTask, wrapper string) error {
	switch {
	case rt.Prompt != "" || rt.Command != "" || rt.Workflow != "" || rt.Script != "":
		body := "prompt"
		switch {
		case rt.Command != "":
			body = "command"
		case rt.Workflow != "":
			body = "workflow"
		case rt.Script != "":
			body = "script"
		}
		return &TaskBodyConflictError{Task: tid, Fields: []string{wrapper, body}}
	case rt.Runtime != "" || rt.Model != "" || rt.Effort != "":
		return fmt.Errorf("task %q: %w", tid, ErrLoopTaskWithRuntime)
	case len(rt.DependsOn) > 0 || rt.When != "" || rt.WritesState != "" ||
		rt.Schema.Kind != 0 || rt.Retry.Kind != 0 ||
		rt.Budget.Kind != 0 || rt.Cache != nil ||
		rt.SystemPrompt != "" || rt.SystemPromptFile != "" || len(rt.OkExit) > 0:
		return fmt.Errorf("task %q: %w", tid, ErrLoopTaskWithFields)
	}
	return nil
}

// DuplicateTaskIDError reports two tasks declaring the same id.
type DuplicateTaskIDError struct{ ID TaskID }

func (e *DuplicateTaskIDError) Error() string {
	return fmt.Sprintf("duplicate task id %q", e.ID)
}

// InvalidWritesStateError reports a `writes_state` value that fails the
// `[A-Za-z0-9_]+` rule, the same alphabet as a state placeholder key.
type InvalidWritesStateError struct {
	Task TaskID
	Key  string
}

func (e *InvalidWritesStateError) Error() string {
	return fmt.Sprintf("task %q: invalid writes_state %q: must match [A-Za-z0-9_]+", e.Task, e.Key)
}

// UnknownDependencyError reports a depends_on entry that does not match any
// task id in the workflow.
type UnknownDependencyError struct {
	Task TaskID
	Dep  TaskID
}

func (e *UnknownDependencyError) Error() string {
	return fmt.Sprintf("task %q: depends on unknown task %q", e.Task, e.Dep)
}

// UnknownPlaceholderError reports a {{x}} placeholder in a prompt whose name
// does not appear in the task's depends_on. Placeholders are not allowed to
// implicitly extend the dependency graph: every templated id must be declared
// up front so the DAG is unambiguous.
//
// Hint, when non-empty, carries a clarifying suggestion appended to the error
// message: currently "did you mean {{params.<name>}}?" when the offending
// bare {{x}} matches a declared param.
type UnknownPlaceholderError struct {
	Task TaskID
	Name string
	Hint string
}

func (e *UnknownPlaceholderError) Error() string {
	msg := fmt.Sprintf("task %q: placeholder {{%s}} not declared in depends_on", e.Task, e.Name)
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	return msg
}

// DuplicateDependencyError reports a task whose depends_on list names the
// same task more than once.
type DuplicateDependencyError struct {
	Task TaskID
	Dep  TaskID
}

func (e *DuplicateDependencyError) Error() string {
	return fmt.Sprintf("task %q: depends_on lists %q more than once", e.Task, e.Dep)
}

// CycleError reports a dependency cycle. Cycle lists the task ids forming the
// cycle in traversal order; the final element is the same as the first.
type CycleError struct{ Cycle []TaskID }

func (e *CycleError) Error() string {
	ids := make([]string, len(e.Cycle))
	for i, id := range e.Cycle {
		ids[i] = string(id)
	}
	return "dependency cycle: " + strings.Join(ids, " -> ")
}

// DuplicateParamNameError reports two params declaring the same name.
type DuplicateParamNameError struct{ Name ParamName }

func (e *DuplicateParamNameError) Error() string {
	return fmt.Sprintf("duplicate param name %q", e.Name)
}

// ConflictingParamSpecError reports a param that sets both `required: true`
// and a `default:`; a default would never apply, so the spec is contradictory.
type ConflictingParamSpecError struct{ Name ParamName }

func (e *ConflictingParamSpecError) Error() string {
	return fmt.Sprintf("param %q: required and default are mutually exclusive", e.Name)
}

// InvalidParamDefaultError reports a param `default:` that fails the
// scalar-string rule (non-scalar YAML node, explicit null, etc.).
type InvalidParamDefaultError struct {
	Name   ParamName
	Reason string
}

func (e *InvalidParamDefaultError) Error() string {
	return fmt.Sprintf("param %q: invalid default: %s", e.Name, e.Reason)
}

// UnknownParamError reports a `{{params.X}}` placeholder whose name is not
// declared in the workflow's `params:` block.
type UnknownParamError struct {
	Task TaskID
	Name string
}

func (e *UnknownParamError) Error() string {
	return fmt.Sprintf("task %q: placeholder {{params.%s}} references undeclared param", e.Task, e.Name)
}

// MalformedParamPlaceholderError reports a `{{params.…}}` token that does not
// match the strict `{{params.name}}` shape, typically `{{params.x.y}}` or
// `{{ params.x }}` with stray whitespace.
type MalformedParamPlaceholderError struct {
	Task  TaskID
	Token string
}

func (e *MalformedParamPlaceholderError) Error() string {
	return fmt.Sprintf("task %q: malformed param placeholder %q", e.Task, e.Token)
}

// SystemPlaceholderTaskRefError reports a `{{taskid}}` placeholder in the
// workflow-level system_prompt. No task can be a dependency of system_prompt,
// so a task reference there is always unresolvable.
type SystemPlaceholderTaskRefError struct {
	Name string
}

func (e *SystemPlaceholderTaskRefError) Error() string {
	return fmt.Sprintf("system_prompt: placeholder {{%s}} references a task; system_prompt has no task dependencies", e.Name)
}

// UnusedParamError reports a declared param that no prompt or system_prompt
// references.
type UnusedParamError struct {
	Name ParamName
}

func (e *UnusedParamError) Error() string {
	return fmt.Sprintf("param %q is declared but never referenced", e.Name)
}

// InvalidRetryMaxError reports a task `retry.max` that is negative. Max counts
// retries after the first attempt, so it must be >= 0.
type InvalidRetryMaxError struct {
	Task TaskID
	Max  int
}

func (e *InvalidRetryMaxError) Error() string {
	return fmt.Sprintf("task %q: invalid retry max %d: must be >= 0", e.Task, e.Max)
}

// UnknownBackoffError reports a task `retry.backoff` that is not one of
// none|constant|exponential.
type UnknownBackoffError struct {
	Task    TaskID
	Backoff string
}

func (e *UnknownBackoffError) Error() string {
	return fmt.Sprintf("task %q: unknown retry backoff %q: must be one of none|constant|exponential", e.Task, e.Backoff)
}

// UnknownRetryClassError reports a task `retry.on` entry that names an error
// class the classifier does not recognize. The recognized vocabulary is
// sourced from RetryClasses so the message cannot drift.
type UnknownRetryClassError struct {
	Task  TaskID
	Class string
}

func (e *UnknownRetryClassError) Error() string {
	return fmt.Sprintf("task %q: unknown retry class %q: only %s is recognized", e.Task, e.Class, recognizedRetryClasses())
}

// UnknownRetryFieldError reports a key inside a task `retry:` mapping that is
// not one of max|backoff|on.
type UnknownRetryFieldError struct {
	Task  TaskID
	Field string
}

func (e *UnknownRetryFieldError) Error() string {
	return fmt.Sprintf("task %q: retry: unknown field %q", e.Task, e.Field)
}

// TaskBodyConflictError reports a task that sets more than one of the five
// mutually exclusive body forms: prompt, prompt_file, command, loop, for_each.
// Fields lists every conflicting key in the order they appear in the YAML
// document, so the error message is deterministic and points directly at the
// offending lines.
//
// TaskBodyConflictError unifies the legacy pairwise sentinels: its Is method
// returns true for ErrPromptAndCommandSet, ErrLoopTaskWithBody, and
// ErrLoopAndForEachSet when Fields contains the corresponding combination, so
// callers using errors.Is against those sentinel values continue to work.
type TaskBodyConflictError struct {
	Task   TaskID
	Fields []string
}

func (e *TaskBodyConflictError) Error() string {
	return fmt.Sprintf("task %q sets mutually exclusive fields: %s", e.Task, strings.Join(e.Fields, ", "))
}

func (e *TaskBodyConflictError) Is(target error) bool {
	has := func(f string) bool { return slices.Contains(e.Fields, f) }
	anyForEach := has("for_each") || has("for_each_parallel")
	switch target {
	case ErrPromptAndCommandSet:
		return has("prompt") && has("command")
	case ErrLoopAndForEachSet:
		return has("loop") && anyForEach
	case ErrLoopTaskWithBody:
		return (has("loop") || anyForEach) && (has("prompt") || has("command") || has("prompt_file"))
	}
	return false
}
