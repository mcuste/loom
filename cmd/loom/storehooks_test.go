package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRunCommand_PersistsExecutorOutputThroughStoreHooks pins the cmd/loom
// pass-through: storeHooks now wires run.OnFinish straight onto
// executor.Hooks.OnFinish with no field-by-field copy, so the executor's
// TaskResult.Output must reach the on-disk record verbatim. The cmd-echo fake
// echoes the substituted prompt as its output, so a correct pass-through
// writes that text into tasks[0].output.
func TestRunCommand_PersistsExecutorOutputThroughStoreHooks(t *testing.T) {
	path := writeWorkflow(t, `
name: wf
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello world
`)
	chdirTo(t, t.TempDir())

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", path})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, buf.String())
	}

	data, err := os.ReadFile(filepath.Join(".loom", "runs", "wf", "latest.json"))
	if err != nil {
		t.Fatalf("read latest.json: %v", err)
	}
	var record struct {
		Tasks []struct {
			Output string `json:"output"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal run record: %v\nraw:\n%s", err, data)
	}
	if len(record.Tasks) != 1 {
		t.Fatalf("len(record.tasks) = %d, want 1", len(record.Tasks))
	}
	if got := record.Tasks[0].Output; got != "hello world" {
		t.Fatalf("tasks[0].output = %q, want %q", got, "hello world")
	}
}
