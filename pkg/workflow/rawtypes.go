package workflow

import (
	"gopkg.in/yaml.v3"
)

// rawWorkflow mirrors the YAML schema as decoded by yaml.v3. It exists only so
// the parser can apply its own validation; callers see the validated Workflow.
//
// Params is captured as a raw yaml.Node so parseParams can inspect each entry's
// `default:` scalar without yaml.v3 coercing `1` to !!int or `~` to !!null
// before validation runs. (Plain decoding into a typed struct would lose the
// distinction between `default: ""` and an absent key.)
type rawWorkflow struct {
	Name         string    `yaml:"name"`
	Description  string    `yaml:"description"`
	Runtime      string    `yaml:"runtime"`
	Model        string    `yaml:"model"`
	Effort       string    `yaml:"effort"`
	SystemPrompt string    `yaml:"system_prompt"`
	Params       yaml.Node `yaml:"params"`
	Tasks        []rawTask `yaml:"tasks"`
	// There is no top-level `loops:` key: loops are declared inline as tasks
	// carrying a `loop:` block (see rawTask.Loop). With KnownFields(true), a
	// stray top-level `loops:` is rejected as an unknown field.
	//
	// Budget is captured as a raw yaml.Node so the parser can distinguish an
	// absent `budget:` key (no limit) from a present block whose max_cost_usd
	// must be validated as a positive float.
	Budget yaml.Node `yaml:"budget"`
	// Cache is the workflow-level memoization default. A plain bool suffices: an
	// absent `cache:` key decodes to false, which is exactly the "off unless a
	// task opts in" default.
	Cache bool `yaml:"cache"`
	// WorkingDir is the cwd every task's child process runs in. A relative value
	// is anchored to the workflow file's directory by AnchorWorkingDir before
	// Parse, so by the time it lands here it is already absolute (or empty).
	WorkingDir string `yaml:"working_dir"`
	// Output names the task whose output is this workflow's result string when it
	// is run as a sub-workflow. Empty selects the lone sink by default; see
	// Workflow.OutputTask. Validated to name a known task.
	Output string `yaml:"output"`
	// Schedule is an optional inline cron schedule reconciled into the schedule
	// store by `loom schedule sync`. A pointer distinguishes an absent block from
	// a present one; both fields are plain strings.
	Schedule *rawSchedule `yaml:"schedule"`
}

// rawSchedule mirrors a workflow's inline `schedule:` block.
type rawSchedule struct {
	Cron string `yaml:"cron"`
	TZ   string `yaml:"tz"`
}

// rawTask mirrors the per-task YAML schema. It exists so the parser can apply
// its own validation before promoting values to the typed Task. Several fields
// are yaml.Node to let the parser distinguish an absent key (zero value, inherit
// default) from a present-but-partial block that must be validated.
type rawTask struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	Runtime     string `yaml:"runtime"`
	Model       string `yaml:"model"`
	Effort      string `yaml:"effort"`
	Prompt      string `yaml:"prompt"`
	Command     string `yaml:"command"`
	// SystemPrompt overrides the workflow-level system_prompt for this task.
	// Empty inherits the workflow default. Mutually exclusive with
	// SystemPromptFile, and meaningless on shell, sub-workflow, and loop-wrapper
	// tasks (the parser rejects it there).
	SystemPrompt string `yaml:"system_prompt"`
	// SystemPromptFile is the file-backed spelling of SystemPrompt, present only
	// when Parse is handed YAML whose `system_prompt_file:` was not inlined by
	// InlinePromptFiles (e.g. a direct Parse call). The normal ParseFile path
	// rewrites it to `system_prompt:` before Parse runs.
	SystemPromptFile string `yaml:"system_prompt_file"`
	// Workflow is the raw registry-name-or-path reference of a sub-workflow task.
	// A non-empty value makes this a sub-workflow leaf: the linked child is run
	// recursively at dispatch. Mutually exclusive with every other body form.
	Workflow string `yaml:"workflow"`
	// Script is the path to an executable run directly at dispatch (honoring its
	// shebang). A non-empty value makes this a script task. Mutually exclusive
	// with every other body form.
	Script string `yaml:"script"`
	// Args is the optional argv passed after Script. Only meaningful on a script
	// task; the parser rejects it elsewhere.
	Args []string `yaml:"args"`
	// OkExit lists non-zero exit codes a command, LLM, or script task treats as
	// success. Rejected on sub-workflow and loop-wrapper tasks.
	OkExit []int `yaml:"ok_exit"`
	// PromptFile is only present when Parse is handed YAML whose `prompt_file:`
	// was not inlined by InlinePromptFiles (e.g. a direct Parse call). It exists
	// so the body-form conflict check can see a `prompt_file` sibling; the normal
	// ParseFile path rewrites it to `prompt:` before Parse ever runs.
	PromptFile  string   `yaml:"prompt_file"`
	DependsOn   []string `yaml:"depends_on"`
	WritesState string   `yaml:"writes_state"`
	When        string   `yaml:"when"`
	// Retry is captured as a raw yaml.Node so the parser can distinguish an
	// absent `retry:` key (zero value, no retry) from a present-but-partial
	// block whose `backoff`/`on` defaults must be filled in.
	Retry yaml.Node `yaml:"retry"`
	// ForEach is captured as a raw yaml.Node so the parser can tell an absent
	// `for_each:` key (a normal prompt/command task) from a present block, which
	// makes this task a for_each wrapper: its id becomes the loop id and its
	// nested `tasks:` the loop body, decoded by decodeForEachBody. A sibling of
	// Loop; a task may set at most one of the two.
	ForEach yaml.Node `yaml:"for_each"`
	// ForEachParallel is the concurrent spelling of ForEach: an identical
	// in/as/tasks block whose body runs once per element in parallel rather than
	// in declaration order. Captured as a raw yaml.Node for the same
	// absent-vs-present reason. A sibling of Loop and ForEach; a task may set at
	// most one of the three.
	ForEachParallel yaml.Node `yaml:"for_each_parallel"`
	// Budget is captured as a raw yaml.Node so the parser can distinguish an
	// absent per-task `budget:` key (no limit) from a present block validated
	// the same way as the workflow-level budget.
	Budget yaml.Node `yaml:"budget"`
	// Schema is captured as a raw yaml.Node so the parser can distinguish an
	// absent per-task `schema:` key (no validation) from a present block whose
	// type/required/properties must be validated.
	Schema yaml.Node `yaml:"schema"`
	// Cache is a pointer so an absent `cache:` key (nil, inherit the workflow
	// default) is distinct from an explicit `cache: false` (opt out). Shell tasks
	// are never memoized regardless, so no shell-vs-LLM rejection applies here.
	Cache *bool `yaml:"cache"`
	// Loop is captured as a raw yaml.Node so the parser can tell an absent
	// `loop:` key (a normal prompt/command task) from a present block, which
	// makes this task a loop wrapper: its id becomes the loop id and its nested
	// `tasks:` the loop body, decoded by decodeLoopBody.
	Loop yaml.Node `yaml:"loop"`
	// With is captured as a raw yaml.Node so the parser can preserve declaration
	// order and validate each key as an identifier when decoding it into the
	// ordered []WithArg. Only meaningful on a sub-workflow task.
	With yaml.Node `yaml:"with"`
}

// rawParam mirrors the typed (non-default) fields of a single `params:`
// entry. `default:` is captured separately as a yaml.Node so the raw scalar
// text (e.g. `1` from `default: 1`) survives without yaml.v3 coercing it to
// !!int, see decodeRawParam.
type rawParam struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
}
