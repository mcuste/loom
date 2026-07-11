// Package launcher turns workflow references into prepared workflow runs.
package launcher

import (
	"context"
	"time"

	"github.com/mcuste/loom/pkg/runner"
)

// Invocation identifies a workflow to run. Ref may be a registry name or a
// filesystem path; only launchers resolve it.
type Invocation struct {
	Ref    string            `json:"ref"`
	Params map[string]string `json:"params,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
}

// Provenance records why a run was launched. Direct CLI runs leave it empty;
// the scheduler supplies its schedule ID, trigger, and fire time.
type Provenance struct {
	ScheduleID  string
	TriggeredBy string
	FireTime    time.Time
}

// Runner is the small port the scheduler uses to start a workflow run.
type Runner interface {
	Launch(context.Context, Invocation, Provenance) (string, error)
}

// Preparer turns an invocation into a validated runner request.
type Preparer interface {
	Prepare(Invocation) (runner.Request, error)
}
