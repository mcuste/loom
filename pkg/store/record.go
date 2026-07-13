package store

import (
	"time"

	"github.com/mcuste/loom/pkg/runtime"
)

// NewTaskRecord constructs a TaskRecord DTO from the fields an executor
// result carries. It converts runtime.Usage to the unexported usageJSON inside
// the store package so callers in pkg/runner need not reference usageJSON
// directly. ID and Iteration are left at zero values for the caller to set.
func NewTaskRecord(prompt, command, output string, exitCode int, elapsed time.Duration, status string, u runtime.Usage) TaskRecord {
	return TaskRecord{
		Prompt:    prompt,
		Command:   command,
		Output:    output,
		ExitCode:  exitCode,
		ElapsedMs: elapsed.Milliseconds(),
		Status:    status,
		Usage:     usageDTO(u),
	}
}

// TriggerType identifies what initiated a run.
type TriggerType string

const (
	TriggerCLI      TriggerType = "cli"
	TriggerSchedule TriggerType = "schedule"
)

// Provenance records the origin of a run. ScheduleID and ScheduledAt are set
// only when Trigger is TriggerSchedule.
type Provenance struct {
	Trigger     TriggerType `json:"trigger,omitempty"`
	ScheduleID  string      `json:"schedule_id,omitempty"`
	ScheduledAt time.Time   `json:"scheduled_at,omitzero"`
}

// RunRecord is the top-level on-disk structure for a single workflow run.
// Exported so callers (e.g. the resume command) bind to the same JSON shape
// the store writes; a field rename here is a compile-time error at the call
// site instead of a silent JSON decode miss.
type RunRecord struct {
	RunID      string `json:"run_id"`
	WorkflowID string `json:"workflow_id"`
	Provenance
	Cwd        string            `json:"cwd,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at,omitzero"`
	ElapsedMs  int64             `json:"elapsed_ms,omitempty"`
	Status     string            `json:"status"`
	Error      string            `json:"error,omitempty"`
	TaskCount  int               `json:"task_count,omitempty"`
	Usage      usageJSON         `json:"usage,omitzero"`
	Manifest   string            `json:"manifest"`
	Params     map[string]string `json:"params,omitempty"`
	Tasks      []TaskRecord      `json:"tasks"`
}

// TaskRecord is the per-task entry within a [RunRecord]. Iteration is the
// 1-based scoped-loop pass that produced this record and 0 for a non-looped
// task, so a looped task contributes one record per pass.
type TaskRecord struct {
	ID         string    `json:"id"`
	Iteration  int       `json:"iteration,omitempty"`
	Runtime    string    `json:"runtime,omitempty"`
	Model      string    `json:"model,omitempty"`
	Effort     string    `json:"effort,omitempty"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	ElapsedMs  int64     `json:"elapsed_ms,omitempty"`
	Status     string    `json:"status,omitempty"`
	Error      string    `json:"error,omitempty"`
	Usage      usageJSON `json:"usage,omitzero"`
	Prompt     string    `json:"prompt,omitempty"`
	Command    string    `json:"command,omitempty"`
	Output     string    `json:"output,omitempty"`
	// ExitCode is a script task's process exit code. Omitted (and zero) for every
	// other task form and for a script task that exited cleanly.
	ExitCode int `json:"exit_code,omitempty"`
}

// usageJSON is unexported; external callers read accounting via the embedded
// fields of RunRecord/TaskRecord rather than through this inner type.
type usageJSON struct {
	InputTokens     int     `json:"input_tokens,omitempty"`
	OutputTokens    int     `json:"output_tokens,omitempty"`
	CacheReadTokens int     `json:"cache_read_tokens,omitempty"`
	TotalCostUSD    float64 `json:"total_cost_usd,omitempty"`
}

func usageDTO(u runtime.Usage) usageJSON {
	return usageJSON{
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens,
		TotalCostUSD:    u.TotalCostUSD,
	}
}
