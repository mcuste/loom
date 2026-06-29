package main

import (
	"strings"
	"testing"
)

// requiredParamWorkflow is the shared fixture for the `loom check` advisory-mode
// tests: a workflow with a single required param so each test can drive one
// resolution outcome (missing, supplied, duplicate) independently.
const requiredParamWorkflow = `
name: wf
runtime: cmd-echo
model: m1
params:
  - name: env
    required: true
tasks:
  - id: a
    prompt: hello {{params.env}}
`

// TestCheckCommandWarnsOnMissingRequired pins that `loom check` with a missing
// required param prints a warning (and the MISSING plan marker) but exits 0.
func TestCheckCommandWarnsOnMissingRequired(t *testing.T) {
	path := writeWorkflow(t, requiredParamWorkflow)

	out, err := runCLI(t, "run", "check", path)
	if err != nil {
		t.Fatalf("check (no -p) returned err = %v; want nil", err)
	}
	if !strings.Contains(out, "warning") || !strings.Contains(out, "env") {
		t.Errorf("expected warning naming `env`; got:\n%s", out)
	}
	if !strings.Contains(out, "MISSING") {
		t.Errorf("expected MISSING marker in plan; got:\n%s", out)
	}
}

// TestCheckCommandResolvesSuppliedRequired pins that supplying the required
// param via -p resolves cleanly: no warning, and a (cli) provenance tag.
func TestCheckCommandResolvesSuppliedRequired(t *testing.T) {
	path := writeWorkflow(t, requiredParamWorkflow)

	out, err := runCLI(t, "run", "check", path, "-p", "env=prod")
	if err != nil {
		t.Fatalf("check (-p env=prod) returned err = %v; want nil", err)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("did not expect warning when env is supplied; got:\n%s", out)
	}
	if !strings.Contains(out, "(cli)") {
		t.Errorf("expected (cli) provenance tag; got:\n%s", out)
	}
}

// TestCheckCommandRejectsDuplicateParam pins that a CLI hygiene error (a
// duplicate -p) hard-fails even in check's otherwise-advisory mode.
func TestCheckCommandRejectsDuplicateParam(t *testing.T) {
	path := writeWorkflow(t, requiredParamWorkflow)

	if _, err := runCLI(t, "run", "check", path, "-p", "env=a", "-p", "env=b"); err == nil {
		t.Fatalf("check with duplicate -p returned nil; want DuplicateCLIParamError")
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

	out, err := runCLI(t, "run", "check", path)
	if err != nil {
		t.Fatalf("check returned err = %v; want nil\noutput:\n%s", err, out)
	}

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
	// runtime= triple must be absent, shell tasks suppress it.
	if strings.Contains(out, "runtime=") {
		t.Errorf("did not expect runtime= in shell-task plan line; got:\n%s", out)
	}
}
