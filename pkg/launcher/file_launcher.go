package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/run"
	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/mcuste/loom/pkg/workflowcheck"
	"github.com/mcuste/loom/pkg/workflowload"
)

// ObserverFactory builds the presentation adapter for one launched run.
type ObserverFactory func(io.Writer) runner.Observer

// Launcher resolves workflow references and prepares them for the runner.
// Schedulers call it only through Runner, while CLI wiring supplies the runtime
// catalog and observer factory.
type Launcher struct {
	Home        string
	Cwd         string
	Catalog     runtime.Catalog
	NewObserver ObserverFactory
	LogRoot     string
	Now         func() time.Time
}

// Prepare loads and validates inv, returning a runner request. It does not
// start the run, so the CLI can show the plan before execution.
func (l Launcher) Prepare(inv Invocation) (runner.Request, error) {
	cwd := inv.Cwd
	if cwd == "" {
		cwd = l.Cwd
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return runner.Request{}, fmt.Errorf("resolve working directory: %w", err)
		}
	}

	wf, manifest, _, err := workflowload.Load(l.Home, cwd, inv.Ref)
	if err != nil {
		return runner.Request{}, fmt.Errorf("load %s: %w", inv.Ref, err)
	}
	resolved, err := workflowcheck.ResolveAndValidateParams(wf, inv.Params, nil, catalogValidator(l.Catalog))
	if err != nil {
		if isParamResolutionError(err) {
			return runner.Request{}, fmt.Errorf("resolve params: %w", err)
		}
		return runner.Request{}, fmt.Errorf("validate routing: %w", err)
	}
	return runner.Request{
		Wf:       wf,
		Manifest: manifest,
		Resolved: resolved,
		Catalog:  l.Catalog,
		Home:     l.Home,
		Cwd:      cwd,
	}, nil
}

// Launch prepares and runs inv, returning the persisted run ID. Scheduled runs
// get their own log file when LogRoot and a schedule ID are configured.
func (l Launcher) Launch(ctx context.Context, inv Invocation, prov Provenance) (string, error) {
	req, err := l.Prepare(inv)
	if err != nil {
		return "", err
	}
	obs, closeLog, err := l.observer(prov)
	if err != nil {
		return "", err
	}
	defer closeLog()
	req.Prov = runner.Provenance{ScheduleID: prov.ScheduleID, TriggeredBy: prov.TriggeredBy}
	return runner.Run(ctx, obs, req)
}

func (l Launcher) observer(prov Provenance) (runner.Observer, func(), error) {
	factory := l.NewObserver
	if factory == nil {
		return noopObserver{}, func() {}, nil
	}
	if l.LogRoot == "" || prov.ScheduleID == "" {
		return factory(io.Discard), func() {}, nil
	}
	fireTime := prov.FireTime
	if fireTime.IsZero() {
		now := time.Now
		if l.Now != nil {
			now = l.Now
		}
		fireTime = now()
	}
	f, err := openLaunchLog(l.LogRoot, prov.ScheduleID, fireTime)
	if err != nil {
		return nil, nil, err
	}
	return factory(f), func() { _ = f.Close() }, nil
}

func openLaunchLog(root, scheduleID string, fireTime time.Time) (*os.File, error) {
	dir := filepath.Join(root, scheduleID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	path := filepath.Join(dir, fireTime.UTC().Format("20060102T150405Z")+".log")
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}
	return f, nil
}

func catalogValidator(catalog runtime.Catalog) runtime.Validator {
	if catalog != nil {
		return catalog
	}
	return runtime.Default()
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

type noopObserver struct{}

func (noopObserver) Header(runner.RunMeta) error { return nil }
func (noopObserver) Events() run.EventSink       { return nil }
func (noopObserver) Summary(*workflow.Workflow, *executor.Report, int) error {
	return nil
}
func (noopObserver) StoreError(error) {}
