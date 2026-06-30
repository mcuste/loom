package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCommand_RecordsInvocationCwd pins that a run records the directory it
// was invoked from in the run record's `cwd` field, so a later resume can
// restore it.
func TestRunCommand_RecordsInvocationCwd(t *testing.T) {
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	path := writeWorkflow(t, `
name: wf
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello
`)

	if out, err := runCLI(t, "run", path); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, out)
	}

	data, err := os.ReadFile(filepath.Join(testRunsDir(t), "wf", "latest.json"))
	if err != nil {
		t.Fatalf("read latest.json: %v", err)
	}
	var record struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal run record: %v\nraw:\n%s", err, data)
	}
	if record.Cwd != cwd {
		t.Errorf("record.cwd = %q, want %q (invocation cwd not recorded)", record.Cwd, cwd)
	}
}

// TestRunCommand_PersistsExecutorOutputThroughStoreHooks pins the cmd/loom
// pass-through: storeHooks wires run.OnFinish straight onto
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
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())

	if out, err := runCLI(t, "run", path); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, out)
	}

	data, err := os.ReadFile(filepath.Join(testRunsDir(t), "wf", "latest.json"))
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

// TestRunCommand_SurfacesTaskFailure pins the run command's error contract: a
// task whose runtime errors on dispatch must make `loom run` return a non-nil
// error (so the process exits non-zero), and the persisted record must mark the
// run failed. cmd-fail errors on Run, and here it is genuinely dispatched (no
// seed bypasses it), so a clean Execute would be a regression.
func TestRunCommand_SurfacesTaskFailure(t *testing.T) {
	path := writeWorkflow(t, `
name: wf
runtime: cmd-fail
model: m1
tasks:
  - id: boom
    prompt: x
`)
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())

	out, err := runCLI(t, "run", path)
	if err == nil {
		t.Fatalf("run of a failing task returned nil error; want failure. output:\n%s", out)
	}

	data, readErr := os.ReadFile(filepath.Join(testRunsDir(t), "wf", "latest.json"))
	if readErr != nil {
		t.Fatalf("read latest.json: %v", readErr)
	}
	var record struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal run record: %v\nraw:\n%s", err, data)
	}
	if record.Status != "failed" {
		t.Errorf("record.status = %q, want %q", record.Status, "failed")
	}
}

// TestRunCommandRejectsUnknownParam pins that the run command refuses a `-p`
// flag whose key is not declared in the workflow's params block. The error
// must surface from ResolveParams before any task runs.
func TestRunCommandRejectsUnknownParam(t *testing.T) {
	path := writeWorkflow(t, `
name: wf
runtime: cmd-echo
model: m1
params:
  - name: env
    default: dev
tasks:
  - id: a
    prompt: hello {{params.env}}
`)
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())

	out, err := runCLI(t, "run", path, "-p", "ghost=x")
	if err == nil {
		t.Fatalf("Execute returned nil; want UnknownCLIParamError. output=%s", out)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the offending param %q", err.Error(), "ghost")
	}
	// Run file should never have been created, bail-out happened before store.Open.
	if _, statErr := os.Stat(testRunsDir(t)); !os.IsNotExist(statErr) {
		t.Errorf("runs directory exists after rejected run; statErr=%v", statErr)
	}
}

// TestRunCommandShellTask drives the full run pipeline for a shell task.
// The progress output must emit the (shell) flavour on start and exit=0 on
// finish; the LLM-specific in=/out=/cache= fields must be absent.
func TestRunCommandShellTask(t *testing.T) {
	path := writeWorkflow(t, `
name: wf_shell
tasks:
  - id: greet
    command: echo hello
`)
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())

	out, err := runCLI(t, "run", path)
	if err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, out)
	}

	// OnStart shell flavour: "[N/N] greet (shell)".
	if !strings.Contains(out, "greet (shell)") {
		t.Errorf("expected 'greet (shell)' in progress; got:\n%s", out)
	}
	// OnFinish shell flavour: "greet done ... exit=0".
	if !strings.Contains(out, "exit=0") {
		t.Errorf("expected 'exit=0' in progress; got:\n%s", out)
	}
	// LLM-specific token fields must be absent.
	if strings.Contains(out, "in=") || strings.Contains(out, "out=") || strings.Contains(out, "cache=") {
		t.Errorf("did not expect token fields for shell task; got:\n%s", out)
	}
}

// TestRunCommandResolvesAndSubstitutes drives the full run pipeline against
// the cmd-echo fake runtime. The persisted run record's tasks[0].prompt must
// equal the substituted text, proving the param flowed through ResolveParams
// → executor.Options.Params → workflow.Substitute → runtime.Request.Prompt.
func TestRunCommandResolvesAndSubstitutes(t *testing.T) {
	path := writeWorkflow(t, `
name: wf
runtime: cmd-echo
model: m1
params:
  - name: who
    default: world
tasks:
  - id: greet
    prompt: hello {{params.who}}
`)
	loomHomeForTest(t)
	chdirTo(t, t.TempDir())

	if out, err := runCLI(t, "run", path, "-p", "who=loom"); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, out)
	}

	// Read the run record via latest.json so we don't have to glob a run id.
	latest := filepath.Join(testRunsDir(t), "wf", "latest.json")
	data, err := os.ReadFile(latest)
	if err != nil {
		t.Fatalf("read latest.json: %v", err)
	}
	var record struct {
		Params map[string]string `json:"params"`
		Tasks  []struct {
			ID     string `json:"id"`
			Prompt string `json:"prompt"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal run record: %v\nraw:\n%s", err, data)
	}
	if got := record.Params["who"]; got != "loom" {
		t.Errorf("record.params[who] = %q, want loom", got)
	}
	if len(record.Tasks) != 1 {
		t.Fatalf("len(record.tasks) = %d, want 1", len(record.Tasks))
	}
	if got := record.Tasks[0].Prompt; got != "hello loom" {
		t.Errorf("tasks[0].prompt = %q, want %q", got, "hello loom")
	}
}
