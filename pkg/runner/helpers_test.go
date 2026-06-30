package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// chdirTo cd's into dir for the rest of the test, restoring the original cwd
// via t.Cleanup.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// runnerTestHome creates a temp dir and returns it as the LOOM_HOME for a
// runner test. The runner receives home directly as Request.Home, so no env
// var is needed; the directory is created to mimic what loomHome() would do.
func runnerTestHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// testRunsDir returns the runs directory for a given home.
func testRunsDir(home string) string {
	return filepath.Join(home, "runs")
}

// readNewRun finds the run record file under <home>/runs/<wfID> whose name is
// not skipID. Used to inspect what a fresh invocation wrote without having to
// predict its (timestamp + random) run id.
func readNewRun(t *testing.T, home, wfID, skipID string) map[string]any {
	t.Helper()
	dir := filepath.Join(testRunsDir(home), wfID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var matches []string
	for _, e := range entries {
		name := e.Name()
		if name == "latest.json" || filepath.Ext(name) != ".json" {
			continue
		}
		if strings.TrimSuffix(name, ".json") == skipID {
			continue
		}
		matches = append(matches, name)
	}
	if len(matches) == 0 {
		t.Fatalf("no new run record produced under %s", dir)
	}
	if len(matches) > 1 {
		t.Fatalf("expected exactly one new run record under %s, found %d: %v", dir, len(matches), matches)
	}
	data, err := os.ReadFile(filepath.Join(dir, matches[0]))
	if err != nil {
		t.Fatalf("read new run: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal new run: %v", err)
	}
	return m
}

// taskField returns the string value of field for the task with the given id in
// a run record decoded as map[string]any, plus whether that task is present at
// all.
func taskField(t *testing.T, rec map[string]any, id, field string) (string, bool) {
	t.Helper()
	tasks, _ := rec["tasks"].([]any)
	for _, raw := range tasks {
		entry, ok := raw.(map[string]any)
		if !ok || entry["id"] != id {
			continue
		}
		val, _ := entry[field].(string)
		return val, true
	}
	return "", false
}

// parseAndResolve is a test helper that parses a manifest and resolves its
// params with the given CLI map and lower-precedence file/record tier.
func parseAndResolve(t *testing.T, manifest string, cli, lower map[string]string) (*workflow.Workflow, workflow.ParamValues) {
	t.Helper()
	wf, err := workflow.Parse([]byte(manifest))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	resolved, err := workflow.ResolveParams(wf, cli, lower)
	if err != nil {
		t.Fatalf("resolve params: %v", err)
	}
	return wf, resolved
}
