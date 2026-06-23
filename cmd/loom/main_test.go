package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

// cmdEchoRuntime is a no-binary fake registered for the cmd/loom smoke tests.
// Its Run returns the substituted prompt verbatim so a test can confirm that
// param substitution happened before the executor dispatched the request.
type cmdEchoRuntime struct{}

func (cmdEchoRuntime) Validate(req runtime.Request) error {
	if req.Model == "" {
		return runtime.ErrMissingModel
	}
	return nil
}

func (cmdEchoRuntime) Run(_ context.Context, req runtime.Request) (runtime.Response, error) {
	return runtime.Response{
		Output: req.Prompt,
		Usage:  runtime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

func init() {
	runtime.Register("cmd-echo", cmdEchoRuntime{})
}

// writeWorkflow drops a workflow YAML into t.TempDir() and returns the path.
func writeWorkflow(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	return path
}

// chdirTo cd's into dir for the rest of the test, restoring the original
// cwd via t.Cleanup. e2e tests pair this with loomHomeForTest so the store
// roots under an isolated $LOOM_HOME and the run's recorded cwd is the temp
// dir rather than the test process's working directory.
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

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", path, "-p", "ghost=x"})

	err := root.Execute()
	if err == nil {
		t.Fatalf("Execute returned nil; want UnknownCLIParamError. output=%s", buf.String())
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the offending param %q", err.Error(), "ghost")
	}
	// Run file should never have been created — bail-out happened before store.Open.
	if _, statErr := os.Stat(testRunsDir(t)); !os.IsNotExist(statErr) {
		t.Errorf("runs directory exists after rejected run; statErr=%v", statErr)
	}
}

// TestCheckCommandWarnsOnMissingRequired pins the `loom check` advisory mode:
// a missing required param prints a warning but exits 0. Supplying the param
// resolves cleanly with no warning. Other CLI hygiene errors (here:
// duplicate `-p`) still hard-fail.
func TestCheckCommandWarnsOnMissingRequired(t *testing.T) {
	path := writeWorkflow(t, `
name: wf
runtime: cmd-echo
model: m1
params:
  - name: env
    required: true
tasks:
  - id: a
    prompt: hello {{params.env}}
`)

	// No -p — expect warning, exit 0.
	{
		var buf bytes.Buffer
		root := newRootCmd()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"run", "check", path})
		if err := root.Execute(); err != nil {
			t.Fatalf("check (no -p) returned err = %v; want nil", err)
		}
		out := buf.String()
		if !strings.Contains(out, "warning") || !strings.Contains(out, "env") {
			t.Errorf("expected warning naming `env`; got:\n%s", out)
		}
		if !strings.Contains(out, "MISSING") {
			t.Errorf("expected MISSING marker in plan; got:\n%s", out)
		}
	}

	// -p env=prod — clean, no warning.
	{
		var buf bytes.Buffer
		root := newRootCmd()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"run", "check", path, "-p", "env=prod"})
		if err := root.Execute(); err != nil {
			t.Fatalf("check (-p env=prod) returned err = %v; want nil", err)
		}
		out := buf.String()
		if strings.Contains(out, "warning") {
			t.Errorf("did not expect warning when env is supplied; got:\n%s", out)
		}
		if !strings.Contains(out, "(cli)") {
			t.Errorf("expected (cli) provenance tag; got:\n%s", out)
		}
	}

	// Duplicate -p — hard error even on check.
	{
		var buf bytes.Buffer
		root := newRootCmd()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs([]string{"run", "check", path, "-p", "env=a", "-p", "env=b"})
		if err := root.Execute(); err == nil {
			t.Fatalf("check with duplicate -p returned nil; want DuplicateCLIParamError")
		}
	}
}

// TestCheckCommandShellTask pins the printPlan output for a workflow that
// contains a shell task. The plan line must show kind=shell and cmd=... but
// must NOT show the runtime/model/effort triple that LLM tasks emit.
func TestCheckCommandShellTask(t *testing.T) {
	path := writeWorkflow(t, `
name: wf_shell
tasks:
  - id: greet
    command: echo hello
`)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", "check", path})

	if err := root.Execute(); err != nil {
		t.Fatalf("check returned err = %v; want nil\noutput:\n%s", err, buf.String())
	}
	out := buf.String()

	// Plan line must contain kind=shell.
	if !strings.Contains(out, "kind=shell") {
		t.Errorf("expected kind=shell in plan; got:\n%s", out)
	}
	// cmd= must quote the command body.
	if !strings.Contains(out, `cmd="echo hello"`) {
		t.Errorf(`expected cmd="echo hello" in plan; got:\n%s`, out)
	}
	// deps=none for a task with no dependencies.
	if !strings.Contains(out, "deps=none") {
		t.Errorf("expected deps=none in plan; got:\n%s", out)
	}
	// runtime= triple must be absent — shell tasks suppress it.
	if strings.Contains(out, "runtime=") {
		t.Errorf("did not expect runtime= in shell-task plan line; got:\n%s", out)
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

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", path})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, buf.String())
	}
	out := buf.String()

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

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", path, "-p", "who=loom"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v\noutput:\n%s", err, buf.String())
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
