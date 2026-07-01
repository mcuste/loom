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
	"slices"

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
// TaskID, or ParamName, and (by extension) the alphabet for `{{id}}` and
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

	// combinedPlaceholderRe matches `{{params.name}}`, `{{state.key}}`,
	// `{{prev.id}}`, `{{id}}`, and `{{id.exit}}` in a single pass. Capture group 1
	// is the param name (non-empty for a param match); group 2 is the state key;
	// group 3 is the prev id; group 4 is the bare task id; group 5 is the task id
	// of an `{{id.exit}}` exit-code reference. Used by Substitute to splice every
	// kind of placeholder in one pass so a substituted value containing
	// `{{taskid}}` text is never re-expanded.
	//
	// The bare-id alternative is listed before the exit alternative, but a bare
	// `(id)` can never satisfy the full pattern for `{{id.exit}}` text (the `.`
	// stops the identifier class and no `}}` follows), so the engine falls through
	// to the exit alternative. Keeping exit last preserves groups 1-4's indices,
	// so Substitute and scanPlaceholders need no reindexing.
	combinedPlaceholderRe = regexp.MustCompile(`\{\{(?:params\.(` + identifierClass + `)|state\.(` + identifierClass + `)|prev\.(` + identifierClass + `)|(` + identifierClass + `)|(` + identifierClass + `)\.exit)\}\}`)

	// prevPlaceholderRe matches `{{prev.id}}` placeholders, which reference the
	// prior iteration's output of a member task inside a scoped loop. The
	// captured name must satisfy identifierClass, the same alphabet as a TaskID.
	prevPlaceholderRe = regexp.MustCompile(`\{\{prev\.(` + identifierClass + `)\}\}`)
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
	// Loops are the workflow's scoped loops in declaration order. Each LoopGroup
	// re-runs a named subgraph of Tasks until its convergence target drains.
	// Empty when the workflow declares no `loops:` block.
	Loops []LoopGroup
	// Budget, when non-nil, caps the workflow's cumulative cost in USD across
	// all completed tasks. nil means no workflow-level cost limit.
	Budget *Budget
	// Cache is the workflow-level memoization default inherited by tasks that do
	// not set their own `cache:` value. false means tasks are not memoized
	// unless they opt in individually.
	Cache bool
	// WorkingDir is the directory every task's child process runs in: the cwd of
	// the LLM runtime, shell, and script processes alike. A relative value is
	// anchored to the workflow file's own directory at load time (see
	// AnchorWorkingDir), so the self-contained manifest runs the same regardless
	// of the cwd at run, resume, or daemon-reload time. Empty means inherit
	// loom's process cwd (the prior behavior). A linked sub-workflow inherits its
	// parent's effective directory unless it sets its own.
	WorkingDir string
	// Output names the task whose output is this workflow's result string when
	// run as a sub-workflow. The zero value selects the lone sink (a task with
	// no dependents) by default; see OutputTask.
	Output TaskID
	// Subs maps each sub-workflow task id to its resolved child workflow. nil
	// when the workflow links no children. Populated by the CLI link step
	// (linkSubWorkflows), never by Parse, which stays filesystem-free.
	Subs map[TaskID]*Workflow
	// Schedule, when non-nil, declares an inline recurring cron schedule that
	// `loom schedule sync` reconciles into the schedule store. Parse captures it
	// structurally; the cron expression's validity is checked by the scheduler,
	// keeping this package free of a cron dependency.
	Schedule *Schedule

	systemPromptTemplate Template

	// byID maps TaskID → index into Tasks for O(1) lookup. Populated by Parse;
	// nil for hand-constructed Workflow values, in which case ByID falls back
	// to a linear scan.
	byID map[TaskID]int
	// paramByName maps ParamName → index into Params for O(1) lookup. Populated
	// by Parse; nil for hand-constructed Workflow values, in which case Param
	// falls back to a linear scan.
	paramByName map[ParamName]int
}

// Schedule is an inline recurring cron schedule declared in a workflow's
// `schedule:` block. Cron is a cron expression; TZ is the optional IANA
// timezone it is evaluated in (empty means the daemon's local time).
type Schedule struct {
	Cron string
	TZ   string
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
	// SystemPrompt overrides Workflow.SystemPrompt for this task when non-empty.
	// Like the workflow-level field it may carry `{{params.x}}` and `{{state.k}}`
	// placeholders (never task-id placeholders) and is substituted at dispatch.
	// Meaningless for shell and sub-workflow tasks, which the parser rejects.
	SystemPrompt string
	// DependsOn names the tasks this task depends on. Populated from explicit
	// `depends_on` in YAML; the parser validates that every `{{id}}` placeholder
	// in the prompt appears here but does not extend this list implicitly.
	DependsOn []TaskID
	// When holds the raw `when:` conditional expression, "" when absent. It is
	// preserved for diagnostics and round-tripping; the executor evaluates the
	// compiled Cond, not this text.
	When string
	// Cond is the compiled form of When, produced by ParseCondition at load
	// time and nil when When is empty. The executor evaluates it once all
	// dependencies resolve and skips the task (status "skipped", empty output)
	// when it evaluates false, still closing the task's gate so downstream tasks
	// proceed. Storing it here avoids re-parsing on every execution.
	Cond *Condition
	// Retry is the task's retry policy. The zero value means "no retry"
	// (Max == 0). Meaningful for both LLM and shell tasks.
	Retry Retry
	// WritesState, when non-empty, names the cross-run state key under which
	// this task's trimmed output is recorded after the run. The executor only
	// reads state for substitution; the write-back is performed by the CLI from
	// Report.Outputs. Must satisfy the identifier alphabet. Allowed on both LLM
	// and shell tasks.
	WritesState string
	// Budget, when non-nil, caps the cumulative cost in USD spent on this task's
	// retries. Once the task's accumulated cost would exceed it, no further
	// retry is attempted. nil means no per-task cost limit.
	Budget *Budget
	// Schema, when non-nil, is the JSON Schema subset the task's output must
	// satisfy. After an LLM task produces output, the executor validates it
	// parses as JSON and matches Schema. nil means no validation. Only LLM
	// tasks may set it; shell tasks are rejected by the parser.
	Schema *Schema
	// Cache, when non-nil, overrides Workflow.Cache for this task: *true opts the
	// task into hash-based output memoization, *false opts it out. nil inherits
	// the workflow-level default. Shell tasks are never memoized regardless.
	Cache *bool
	// Loop is the id of the scoped loop this task belongs to, or "" for a
	// top-level task. A task is defined in exactly one place, so it belongs to at
	// most one loop. Set by Parse from the enclosing `loops:` entry.
	Loop LoopID
	// Workflow is the raw registry-name-or-path reference of a sub-workflow task:
	// at dispatch the executor recursively runs the linked child and captures its
	// result. Empty for every other task body form. Mutually exclusive with
	// Prompt, Command, and the loop wrappers.
	Workflow string
	// With carries the values passed to the linked child's params, in declaration
	// order (a slice keeps the order deterministic). Each value is substituted
	// with the parent context before it is handed to the child as a CLI-tier
	// param value. Empty unless Workflow is set.
	With []WithArg
	// Script is the path to an executable script run directly (honoring its
	// shebang) at dispatch. Mutually exclusive with Prompt, Command, and Workflow.
	// The path and every Args entry carry `{{id}}` / `{{params.x}}` / `{{state.k}}`
	// placeholders substituted before execution. Unlike a shell task, a non-zero
	// exit is captured as data (TaskResult.ExitCode, readable downstream via
	// `{{id.exit}}`) rather than failing the task; only a launch failure (missing
	// file, not executable) is an error. Like a shell task it takes no runtime,
	// model, effort, system prompt, or schema.
	Script string
	// Args is the optional argv passed after Script, each substituted like the
	// path. Empty unless Script is set.
	Args []string
	// OkExit lists non-zero process exit codes that count as success rather than
	// failure for a command, LLM, or script task. Exit 0 is always success; a
	// code in OkExit makes the task succeed with its ExitCode captured (readable
	// downstream via `{{id.exit}}`); any other non-zero code fails the task. When
	// OkExit is nil (unset) the defaults apply: a script task tolerates every exit
	// code (its exit is data), while a command or LLM task tolerates only 0.
	// Rejected on sub-workflow and loop-wrapper tasks, which have no process exit.
	OkExit []int

	// action is the typed body form produced by Parse. It shadows the public
	// legacy fields above until callers migrate.
	action               Action
	systemPromptTemplate Template
}

// WithArg is one `with:` entry on a sub-workflow task: a child param name and
// the (pre-substitution) value passed for it.
type WithArg struct {
	Name ParamName
	// Value is the raw value text, substituted with the parent context before
	// being passed to the child.
	Value string
}

// IsSubWorkflow reports whether t links and runs another workflow (has Workflow
// set) rather than carrying a prompt, command, or loop body. The parser
// enforces that Workflow is mutually exclusive with every other body form, so
// this is a reliable discriminator at the executor and CLI layers.
func (t Task) IsSubWorkflow() bool { return t.Workflow != "" }

// BodyKind names the single body form a Task carries. A valid task sets exactly
// one of Prompt, Command, Workflow, or Script; BodyKind is the discriminator the
// executor routes on, replacing an implicit precedence among the optional body
// strings. BodyInvalid marks a task that set none or more than one, an illegal
// shape the parser rejects: routing on it crashes early instead of silently
// misdispatching a prompt+command or prompt+workflow task down the wrong path.
type BodyKind int

const (
	// BodyInvalid is the zero value: a task with no body, or more than one set.
	BodyInvalid BodyKind = iota
	// BodyPrompt is an LLM task carrying a Prompt.
	BodyPrompt
	// BodyShell is a shell task carrying a Command.
	BodyShell
	// BodySubWorkflow is a task linking and running another Workflow.
	BodySubWorkflow
	// BodyScript is a task running an executable Script file directly.
	BodyScript
)

// BodyKind reports which body form t carries, returning BodyInvalid when t sets
// none or more than one of Prompt, Command, Workflow, and Script.
func (t Task) BodyKind() BodyKind {
	if t.action != nil {
		return bodyKindForAction(t.action)
	}
	return bodyKindFromFields(t)
}

func bodyKindFromFields(t Task) BodyKind {
	n := 0
	kind := BodyInvalid
	if t.Prompt != "" {
		n++
		kind = BodyPrompt
	}
	if t.Command != "" {
		n++
		kind = BodyShell
	}
	if t.Workflow != "" {
		n++
		kind = BodySubWorkflow
	}
	if t.Script != "" {
		n++
		kind = BodyScript
	}
	if n != 1 {
		return BodyInvalid
	}
	return kind
}

// OutputTask returns the id of the task whose output is the workflow's result
// string when it is run as a sub-workflow. It returns Output when set (after
// validating it names a known task); otherwise it returns the lone sink (a task
// that no other task depends on). Zero or multiple sinks with no explicit
// Output is an error.
func (w *Workflow) OutputTask() (TaskID, error) {
	if w.Output != "" {
		if w.ByID(w.Output) == nil {
			return "", &UnknownOutputTaskError{Task: w.Output}
		}
		return w.Output, nil
	}
	// A sink is a task that appears in no other task's DependsOn. Build the
	// dependents set from the same adjacency Plan derives, then keep the tasks
	// no one depends on, in declaration order.
	hasDependent := make(map[TaskID]bool, len(w.Tasks))
	for _, t := range w.Tasks {
		for _, d := range t.DependsOn {
			hasDependent[d] = true
		}
	}
	var sinks []TaskID
	for _, t := range w.Tasks {
		if !hasDependent[t.ID] {
			sinks = append(sinks, t.ID)
		}
	}
	switch len(sinks) {
	case 1:
		return sinks[0], nil
	case 0:
		return "", fmt.Errorf("workflow %q: no terminal task to use as output, set output to pick one", w.ID)
	default:
		return "", fmt.Errorf("workflow %q: %d terminal tasks; set output: to pick one", w.ID, len(sinks))
	}
}

// UnknownOutputTaskError reports a top-level `output:` that names a task absent
// from the workflow.
type UnknownOutputTaskError struct{ Task TaskID }

func (e *UnknownOutputTaskError) Error() string {
	return fmt.Sprintf("output: names unknown task %q", e.Task)
}

// Backoff names the delay schedule applied between retry attempts.
type Backoff string

const (
	// BackoffNone retries with no delay between attempts.
	BackoffNone Backoff = "none"
	// BackoffConstant waits a fixed base delay before every retry.
	BackoffConstant Backoff = "constant"
	// BackoffExponential doubles the base delay each retry (base, 2*base, ...).
	BackoffExponential Backoff = "exponential"
)

// Retry is a per-task retry policy. Zero value means no retry.
type Retry struct {
	// Max is the number of retries after the first attempt. 0 disables retry.
	Max int
	// Backoff selects the inter-attempt delay schedule.
	Backoff Backoff
	// On lists the error classes that are retryable. Only "transient" is
	// recognized for now.
	On []string
}

// Enabled reports whether the policy permits at least one retry (Max > 0).
func (r Retry) Enabled() bool {
	return r.Max > 0
}

// IsShell reports whether t is a shell task (has Command set) rather than an
// LLM task. The parser enforces XOR between Prompt and Command, so this is a
// reliable discriminator at the executor, CLI, and store layers.
func (t Task) IsShell() bool { return t.Command != "" }

// IsScript reports whether t is a script task (has Script set) that runs an
// executable file directly. The parser enforces Script is mutually exclusive
// with every other body form, so this is a reliable discriminator at the
// executor, CLI, and TUI layers.
func (t Task) IsScript() bool { return t.Script != "" }

// ExitTolerated reports whether process exit code is a success for t rather than
// a failure. Exit 0 is always success. When OkExit is set, exactly the listed
// codes (plus 0) are success and every other code fails. When OkExit is unset, a
// script task tolerates every code (its exit is data) while every other task
// tolerates only 0. A negative code (a signal kill, which exec reports as -1)
// can never appear in OkExit (the parser bounds entries to 0-255), so a killed
// process always fails.
func (t Task) ExitTolerated(code int) bool {
	if code == 0 {
		return true
	}
	if code < 0 {
		// A negative code means the process was killed by a signal (exec reports
		// -1) or never produced an exit status. That is never a tolerated outcome,
		// not even under a script task's tolerate-everything default, so a
		// cancelled or signal-killed task always fails.
		return false
	}
	if t.OkExit != nil {
		return slices.Contains(t.OkExit, code)
	}
	return t.BodyKind() == BodyScript
}

// TextBodies returns every substitutable text fragment a task carries: its
// prompt or command body, followed by each with-value. A shell task keeps its
// body in Command; a script task carries its substitutable text in Script and
// Args; a sub-workflow task has no prompt body for this form (prompt is empty),
// so its prev and placeholder refs live in the with-values instead. Centralizing
// the body-form discrimination here keeps placeholder scanning from drifting as
// new body forms are added.
func (t Task) TextBodies() []string {
	var body string
	switch t.BodyKind() {
	case BodyPrompt:
		body = t.Prompt
	case BodyShell:
		body = t.Command
	case BodyScript:
		body = t.Script
	case BodySubWorkflow, BodyInvalid:
		// A sub-workflow carries its refs in the with-values, not a prompt body;
		// an invalid task has no single body to scan.
		body = ""
	}
	bodies := make([]string, 0, 1+len(t.Args)+len(t.With))
	if body != "" {
		bodies = append(bodies, body)
	}
	// A script task's argv entries carry placeholders too, so they must be scanned
	// alongside the path.
	bodies = append(bodies, t.Args...)
	for _, a := range t.With {
		bodies = append(bodies, a.Value)
	}
	return bodies
}

// Param is a declared workflow parameter: a named value supplied at run time
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
