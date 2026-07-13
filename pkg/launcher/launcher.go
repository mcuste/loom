package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
	"github.com/mcuste/loom/pkg/workflowcheck"
	"github.com/mcuste/loom/pkg/workflowload"
)

// RunOutputFactory builds the output adapter for one launched run.
type RunOutputFactory func(io.Writer) runner.RunOutput

// RunRequest identifies a workflow to run. Ref may be a registry name or a
// filesystem path; only launchers resolve it.
type RunRequest struct {
	Ref    string            `json:"ref"`
	Params map[string]string `json:"params,omitempty"`
	Cwd    string            `json:"cwd,omitempty"`
}

// RunLauncher is the small port the daemon uses to start a workflow run.
type RunLauncher interface {
	Launch(context.Context, RunRequest, store.Provenance) (string, error)
}

// Preparer turns a run request into a validated runner request.
type Preparer interface {
	Prepare(RunRequest) (runner.Request, error)
}

// Launcher resolves workflow references and prepares them for the runner.
// The daemon calls it only through RunLauncher, while CLI wiring supplies the runtime
// catalog and run-output factory.
type Launcher struct {
	// Home is Loom's data directory.
	Home string
	// Cwd resolves refs and records the run; it is not the task cwd.
	Cwd string
	// Catalog selects and validates runtimes.
	Catalog runtime.Catalog
	// NewOutput builds output for a launched run.
	NewOutput RunOutputFactory
	// LogRoot holds scheduled-run logs.
	LogRoot string
	// Now timestamps scheduled-run logs.
	Now func() time.Time
}

// Prepare loads and validates request, returning a runner request. It does not
// start the run, so the CLI can show the plan before execution.
func (l Launcher) Prepare(request RunRequest) (runner.Request, error) {
	cwd := request.Cwd
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

	wf, manifest, _, err := workflowload.Load(l.Home, cwd, request.Ref)
	if err != nil {
		return runner.Request{}, fmt.Errorf("load %s: %w", request.Ref, err)
	}
	resolved, err := workflowcheck.ResolveAndValidateParams(wf, request.Params, nil, catalogValidator(l.Catalog))
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
		Prov:     store.Provenance{Trigger: store.TriggerCLI},
	}, nil
}

// Launch prepares and runs request, returning the persisted run ID. Scheduled
// runs get their own log file when LogRoot and a schedule ID are configured.
func (l Launcher) Launch(ctx context.Context, request RunRequest, provenance store.Provenance) (string, error) {
	req, err := l.Prepare(request)
	if err != nil {
		return "", err
	}
	output, closeLog, err := l.output(provenance)
	if err != nil {
		return "", err
	}
	defer closeLog()
	req.Prov = provenance
	return runner.Run(ctx, output, req)
}

func (l Launcher) output(prov store.Provenance) (runner.RunOutput, func(), error) {
	factory := l.NewOutput
	if factory == nil {
		return nil, nil, errors.New("launcher run output factory is required")
	}
	if l.LogRoot == "" || prov.ScheduleID == "" {
		return factory(io.Discard), func() {}, nil
	}
	scheduledAt := prov.ScheduledAt
	if scheduledAt.IsZero() {
		now := time.Now
		if l.Now != nil {
			now = l.Now
		}
		scheduledAt = now()
	}
	f, err := openLaunchLog(l.LogRoot, prov.ScheduleID, scheduledAt)
	if err != nil {
		return nil, nil, err
	}
	return factory(f), func() { _ = f.Close() }, nil
}

func openLaunchLog(root, scheduleID string, scheduledAt time.Time) (*os.File, error) {
	dir := filepath.Join(root, scheduleID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	path := filepath.Join(dir, scheduledAt.UTC().Format("20060102T150405Z")+".log")
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
