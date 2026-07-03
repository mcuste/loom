// Package interpreter defines the application seam for parsing, validating,
// compiling, and launching workflow programs.
package interpreter

import (
	"context"
	"time"
)

// WorkflowRef is an opaque reference to a workflow program. It may be a
// registry name or a filesystem path; only interpreter launchers resolve it.
type WorkflowRef string

// RunID identifies a persisted interpreter run.
type RunID string

// WorkflowInvocation is the scheduler's opaque request to launch a workflow.
// The scheduler stores and forwards it, but does not inspect workflow tasks,
// DAGs, runtimes, gates, reports, or loop metadata.
type WorkflowInvocation struct {
	Ref    WorkflowRef       `json:"ref"`
	Params map[string]string `json:"params,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
}

// Provenance records why a run was launched. Direct CLI runs usually leave it
// empty; the scheduler sets ScheduleID, TriggeredBy, and FireTime.
type Provenance struct {
	ScheduleID  string
	TriggeredBy string
	FireTime    time.Time
}

// RunLauncher is the small port the scheduler uses to enter the interpreter.
// Implementations own workflow loading, parameter validation, renderer setup,
// run persistence, and runtime catalog wiring.
type RunLauncher interface {
	Launch(context.Context, WorkflowInvocation, Provenance) (RunID, error)
}
