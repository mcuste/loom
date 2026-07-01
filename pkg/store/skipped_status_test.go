package store_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestOnFinishPreservesSkippedStatus pins the live/replay parity contract: a
// task whose `when:` guard skipped it carries StatusSkipped on its result and a
// nil run error. The persisted record must record that skipped disposition,
// not collapse it to "ok" the way a successful task is recorded. Otherwise the
// live TUI (reading the executor result) and the on-disk run view (reading this
// record) disagree about the same task.
func TestOnFinishPreservesSkippedStatus(t *testing.T) {
	root := t.TempDir()
	run, err := store.Open("wf", []byte("name: wf\n"), store.Config{Root: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	task := workflow.Task{ID: "guarded"}
	// Match the executor's hook sequence: OnStart initialises the task entry,
	// then OnFinish records its terminal disposition.
	run.OnStart(task, 0, "", "", "")
	run.OnFinish(task, 0, store.TaskRecord{Status: store.StatusSkipped}, nil)

	tasks := readRun(t, run.Path())["tasks"].([]any)
	got := tasks[0].(map[string]any)
	// Assert against the literal on-disk string, not a re-exported constant, so a
	// rename that changes the serialized value is caught here.
	if got["status"] != "skipped" {
		t.Fatalf("persisted status = %v, want %q (skipped must not be recorded as ok)", got["status"], "skipped")
	}
}
