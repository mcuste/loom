package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/mcuste/loom/pkg/interpreter"
	"github.com/mcuste/loom/pkg/schedule"
)

// fireResult reports a finished fire back to the loop.
type fireResult struct {
	scheduleID string
	oneOff     bool
	fireTime   time.Time
	runID      string
	err        error
}

// execute launches the schedule's opaque workflow invocation through the
// interpreter port. The daemon does not load workflow YAML, validate params,
// inspect tasks, or configure runtimes here; those are interpreter concerns.
func (d *daemon) execute(ctx context.Context, rec schedule.Record, fireTime time.Time, results chan<- fireResult) {
	res := fireResult{scheduleID: rec.ID, oneOff: !rec.Trigger.IsCron(), fireTime: fireTime}
	defer func() { results <- res }()

	if d.launcher == nil {
		res.err = fmt.Errorf("schedule %s: run launcher is nil", rec.ID)
		d.logf("schedule %s: %v", rec.ID, res.err)
		return
	}
	prov := interpreter.Provenance{ScheduleID: rec.ID, TriggeredBy: "schedule", FireTime: fireTime}
	runID, err := d.launcher.Launch(ctx, rec.Invocation(d.cwd), prov)
	res.runID = string(runID)
	res.err = err
	if err != nil {
		d.logf("schedule %s: run failed: %v", rec.ID, err)
		return
	}
	d.logf("schedule %s: run %s complete", rec.ID, res.runID)
}
