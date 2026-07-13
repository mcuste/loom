package launcher_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/launcher"
	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/tui"
	"github.com/mcuste/loom/pkg/workflow"
)

func TestLaunchWithoutOutputFactoryReturnsConfigurationError(t *testing.T) {
	sut, request := launcherForWorkflow(t, `
name: missing_output
tasks:
  - id: greet
    command: echo hello
`)

	_, err := sut.Launch(t.Context(), request, store.Provenance{})

	if err == nil || err.Error() != "launcher run output factory is required" {
		t.Fatalf("Launch error = %v, want missing output factory error", err)
	}
}

func TestScheduledLaunchWritesLogAndPersistsProvenance(t *testing.T) {
	sut, request := launcherForWorkflow(t, `
name: scheduled
tasks:
  - id: greet
    command: echo hello
`)
	logRoot := t.TempDir()
	scheduledAt := time.Date(2026, 7, 11, 12, 30, 0, 0, time.UTC)
	sut.LogRoot = logRoot
	sut.NewOutput = func(w io.Writer) runner.RunOutput { return tui.New(w) }
	provenance := store.Provenance{
		Trigger:     store.TriggerSchedule,
		ScheduleID:  "daily_report",
		ScheduledAt: scheduledAt,
	}

	runID, err := sut.Launch(t.Context(), request, provenance)

	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	logPath := filepath.Join(logRoot, "daily_report", "20260711T123000Z.log")
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read scheduled run log: %v", err)
	}
	if !strings.Contains(string(log), `workflow "scheduled" complete`) {
		t.Errorf("scheduled run log does not contain completion summary:\n%s", log)
	}
	record, err := store.Load(filepath.Join(sut.Home, "runs", "scheduled", runID+".json"))
	if err != nil {
		t.Fatalf("load run record: %v", err)
	}
	if record.Trigger != store.TriggerSchedule || record.ScheduleID != "daily_report" || !record.ScheduledAt.Equal(scheduledAt) {
		t.Errorf("run provenance = %#v, want scheduled daily_report at %s", record.Provenance, scheduledAt)
	}
}

func TestPrepareWithMissingRequiredParamReturnsResolutionError(t *testing.T) {
	sut, request := launcherForWorkflow(t, `
name: required_param
params:
  - name: env
    required: true
tasks:
  - id: greet
    command: echo {{params.env}}
`)

	_, err := sut.Prepare(request)

	var missing *workflow.MissingRequiredParamError
	if !errors.As(err, &missing) {
		t.Fatalf("Prepare error = %v, want MissingRequiredParamError", err)
	}
	if !strings.HasPrefix(err.Error(), "resolve params: ") {
		t.Errorf("Prepare error = %q, want resolve params prefix", err)
	}
}

func TestPrepareWithUnknownRuntimeReturnsRoutingError(t *testing.T) {
	sut, request := launcherForWorkflow(t, `
name: unknown_runtime
runtime: unavailable-runtime
model: m1
tasks:
  - id: greet
    prompt: hello
`)
	sut.Catalog = &runtime.Registry{}

	_, err := sut.Prepare(request)

	if !errors.Is(err, runtime.ErrUnknownRuntime) {
		t.Fatalf("Prepare error = %v, want unknown runtime error", err)
	}
	if !strings.HasPrefix(err.Error(), "validate routing: ") {
		t.Errorf("Prepare error = %q, want validate routing prefix", err)
	}
}

func launcherForWorkflow(t *testing.T, manifest string) (launcher.Launcher, launcher.RunRequest) {
	t.Helper()
	home := t.TempDir()
	cwd := t.TempDir()
	path := filepath.Join(cwd, "workflow.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return launcher.Launcher{Home: home, Cwd: cwd}, launcher.RunRequest{Ref: path, Cwd: cwd}
}
