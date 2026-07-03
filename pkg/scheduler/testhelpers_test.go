package scheduler

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/interpreter"
	"github.com/mcuste/loom/pkg/runner"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/schedule"
	"github.com/mcuste/loom/pkg/tui"
)

func newTestDaemon(home, cwd string, catalog runtime.Catalog, out io.Writer) *daemon {
	launcher := interpreter.FileRunLauncher{
		Home:    home,
		Cwd:     cwd,
		Catalog: catalog,
		NewObserver: func(w io.Writer) runner.Observer {
			return tui.New(w)
		},
		LogRoot: filepath.Join(schedule.SchedulesDir(home), "logs"),
	}
	return New(home, cwd, launcher, out)
}

// fixedClock returns a deterministic clock so the daemon's firing decision is
// reproducible without depending on wall time.
func fixedClock(ts string) func() time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

// counterRand returns a deterministic six-hex suffix for schedule ids.
func counterRand(initial uint32) func() (string, error) {
	var n atomic.Uint32
	n.Store(initial)
	return func() (string, error) {
		v := n.Add(1) - 1
		return fmt.Sprintf("%06x", v), nil
	}
}

// writeWorkflow drops a workflow YAML into a temp dir and returns the path.
func writeWorkflow(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

// awaitResult blocks until a fireResult arrives on results or 30s elapses.
func awaitResult(t *testing.T, results <-chan fireResult) fireResult {
	t.Helper()
	select {
	case res := <-results:
		return res
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for fire result")
		return fireResult{}
	}
}

// waitForCondition polls cond until it holds or a 30s deadline elapses,
// failing the test on timeout.
func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 30s")
}
