package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// countRunRecords returns the number of persisted run records under
// .loom/runs/<wfID>, excluding the latest.json symlink. The loop wrapper
// writes one record per iteration, so this is the iteration count.
func countRunRecords(t *testing.T, wfID string) int {
	t.Helper()
	dir := filepath.Join(".loom", "runs", wfID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if name == "latest.json" || filepath.Ext(name) != ".json" {
			continue
		}
		n++
	}
	return n
}

// TestRunWorkflow_LoopDrainsAndStops pins loop-until-dry: a shell tick task
// decrements a counter file and emits work while the counter is positive,
// then empty output once drained. The loop must stop on the first empty
// until_empty output and write one run record per iteration.
func TestRunWorkflow_LoopDrainsAndStops(t *testing.T) {
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
loop:
  until_empty: tick
  max: 10
tasks:
  - id: tick
    command: 'n=$(cat n 2>/dev/null || echo 3); n=$((n-1)); echo $n > n; if [ $n -gt 0 ]; then echo work-$n; fi'
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, nil, seedPlan{}); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}
	// Counter starts at 3: iterations emit work-2, work-1, then empty -> 3 runs.
	if got := countRunRecords(t, "wf"); got != 3 {
		t.Errorf("run records = %d, want 3 (loop should drain on iteration 3)\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "── iteration 1/10 ──") {
		t.Errorf("loop did not print the iteration header:\n%s", buf.String())
	}
}

// TestRunWorkflow_LoopRespectsMax pins that a loop whose until_empty task never
// empties stops after exactly Max iterations.
func TestRunWorkflow_LoopRespectsMax(t *testing.T) {
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
loop:
  until_empty: tick
  max: 3
tasks:
  - id: tick
    command: echo always
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, nil, seedPlan{}); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}
	if got := countRunRecords(t, "wf"); got != 3 {
		t.Errorf("run records = %d, want 3 (Max cap)\n%s", got, buf.String())
	}
}

// TestRunWorkflow_LoopCarriesState pins that cross-run state written by one
// iteration is visible to the next. The tick task echoes the prior state value
// plus one char and records it; it drains (empty output) once the value
// reaches "xxx". With max=10 the loop must stop at iteration 4 on drain, not
// on the cap, proving state carried between iterations.
func TestRunWorkflow_LoopCarriesState(t *testing.T) {
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
loop:
  until_empty: tick
  max: 10
tasks:
  - id: tick
    command: 'prev="{{state.seen}}"; if [ "$prev" = "xxx" ]; then exit 0; else echo "${prev}x"; fi'
    writes_state: seen
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, nil, seedPlan{}); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}
	// "" -> x -> xx -> xxx (drain on the 4th pass).
	if got := countRunRecords(t, "wf"); got != 4 {
		t.Errorf("run records = %d, want 4 (state-driven drain before Max)\n%s", got, buf.String())
	}
}

// TestRunWorkflow_LoopBypassedOnResume pins that a seeded run (resume) is
// single-shot even when the workflow declares a loop: the loop wrapper is
// bypassed and exactly one run record is written, despite the until_empty task
// never emptying.
func TestRunWorkflow_LoopBypassedOnResume(t *testing.T) {
	chdirTo(t, t.TempDir())

	manifest := `name: wf
model: m1
loop:
  until_empty: tick
  max: 5
tasks:
  - id: seed
    runtime: cmd-echo
    prompt: seeded-body
  - id: tick
    depends_on: [seed]
    command: echo always
`
	wf, resolved := parseAndResolve(t, manifest, nil, nil)

	plan := seedPlan{
		seed:    map[workflow.TaskID]string{"seed": "stored-seed"},
		entries: []seedEntry{{id: "seed", prompt: "seeded-body", output: "stored-seed"}},
	}

	var buf bytes.Buffer
	if err := runWorkflow(&buf, []byte(manifest), wf, resolved, nil, plan); err != nil {
		t.Fatalf("runWorkflow: %v\noutput:\n%s", err, buf.String())
	}
	if got := countRunRecords(t, "wf"); got != 1 {
		t.Errorf("run records = %d, want 1 (resume bypasses the loop)\n%s", got, buf.String())
	}
	if strings.Contains(buf.String(), "── iteration") {
		t.Errorf("resume should not print loop iteration headers:\n%s", buf.String())
	}
}
