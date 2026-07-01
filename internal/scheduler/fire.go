package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/mcuste/loom/pkg/workflowcheck"
	"github.com/mcuste/loom/pkg/workflowload"
)

// fireResult reports a finished fire back to the loop.
type fireResult struct {
	scheduleID string
	oneOff     bool
	fireTime   time.Time
	runID      string
	err        error
}

// execute reloads the workflow fresh, resolves its params against the current
// definition, and runs it, streaming output to a per-fire log file. The run is
// recorded in the normal run store; the captured run id flows back via results.
func (d *daemon) execute(rec schedule.Record, fireTime time.Time, results chan<- fireResult) {
	res := fireResult{scheduleID: rec.ID, oneOff: !rec.Trigger.IsCron(), fireTime: fireTime}
	defer func() { results <- res }()

	wf, manifest, _, err := workflowload.Load(d.home, d.cwd, rec.Path)
	if err != nil {
		res.err = fmt.Errorf("load %s: %w", rec.Path, err)
		d.logf("schedule %s: %v", rec.ID, res.err)
		return
	}
	resolved, err := workflowcheck.ResolveAndValidateParams(wf, rec.Params, nil, d.catalog)
	if err != nil {
		if isParamResolutionError(err) {
			res.err = fmt.Errorf("resolve params: %w", err)
		} else {
			res.err = fmt.Errorf("validate routing: %w", err)
		}
		d.logf("schedule %s: %v", rec.ID, res.err)
		return
	}

	logPath, lf, err := d.openLog(rec.ID, fireTime)
	if err != nil {
		res.err = err
		d.logf("schedule %s: %v", rec.ID, err)
		return
	}
	defer func() { _ = lf.Close() }()

	cwd, err := os.Getwd()
	if err != nil {
		res.err = fmt.Errorf("resolve working directory: %w", err)
		d.logf("schedule %s: %v", rec.ID, res.err)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	prov := runner.Provenance{ScheduleID: rec.ID, TriggeredBy: "schedule"}
	r := tui.New(lf)
	defer func() { _ = r.Close() }()
	res.runID, res.err = runner.Run(ctx, r, runner.Request{
		Wf:       wf,
		Manifest: manifest,
		Resolved: resolved,
		Catalog:  d.catalog,
		Home:     d.home,
		Cwd:      cwd,
		Prov:     prov,
	})
	if res.err != nil {
		d.logf("schedule %s: run failed (see %s): %v", rec.ID, logPath, res.err)
	} else {
		d.logf("schedule %s: run %s complete (log %s)", rec.ID, res.runID, logPath)
	}
}

func isParamResolutionError(err error) bool {
	var missing *workflow.MissingRequiredParamError
	if errors.As(err, &missing) {
		return true
	}
	var unknownCLI *workflow.UnknownCLIParamError
	if errors.As(err, &unknownCLI) {
		return true
	}
	var unknownFile *workflow.UnknownFileParamError
	return errors.As(err, &unknownFile)
}

// openLog creates (and returns) the per-fire log file under
// <home>/schedules/logs/<id>/<fire-timestamp>.log.
func (d *daemon) openLog(id string, fireTime time.Time) (string, *os.File, error) {
	dir := d.scheduleLogDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create log dir: %w", err)
	}
	path := filepath.Join(dir, fireTime.UTC().Format("20060102T150405Z")+".log")
	f, err := os.Create(path)
	if err != nil {
		return "", nil, fmt.Errorf("create log file: %w", err)
	}
	return path, f, nil
}
