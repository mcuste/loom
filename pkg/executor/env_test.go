package executor_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/workflow"
)

// hasEnv reports whether env carries a `name=want` entry, honoring exec's
// last-wins rule for a duplicated key.
func hasEnv(env []string, name, want string) bool {
	got, ok := "", false
	for _, e := range env {
		if k, v, found := strings.Cut(e, "="); found && k == name {
			got, ok = v, true
		}
	}
	return ok && got == want
}

// TestTaskEnvBareNames pins the bare-name scheme: an upstream output is exposed
// as $id, a script exit code as $id_exit, params/state by their bare names, a
// prev-iteration output as $prev_id, and the for_each loop variable by its `as`
// name.
func TestTaskEnvBareNames(t *testing.T) {
	env := executor.TaskEnv(
		map[workflow.TaskID]string{"lint": "findings"},
		workflow.ParamValues{"env": "prod"},
		map[string]string{"last_sha": "abc"},
		map[workflow.TaskID]string{"draft": "v0"},
		map[workflow.TaskID]int{"lint": 2},
		"item", "x",
	)
	for _, c := range []struct{ name, want string }{
		{"lint", "findings"},
		{"lint_exit", "2"},
		{"env", "prod"},
		{"last_sha", "abc"},
		{"prev_draft", "v0"},
		{"item", "x"},
	} {
		if !hasEnv(env, c.name, c.want) {
			t.Errorf("env missing %s=%q", c.name, c.want)
		}
	}
}

// TestTaskEnvSkipsLeadingDigit pins that an id which is not a valid shell
// variable name (leading digit) is skipped rather than emitted as an
// unreferenceable entry.
func TestTaskEnvSkipsLeadingDigit(t *testing.T) {
	env := executor.TaskEnv(
		map[workflow.TaskID]string{"2nd": "v"},
		nil, nil, nil, nil, "", "",
	)
	if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "2nd=") }) {
		t.Errorf("env should skip invalid name 2nd, got %v", env)
	}
}

// TestTaskEnvOutputWinsOverParam pins the documented precedence: when a task id
// and a param share a name, the current output wins (appended last).
func TestTaskEnvOutputWinsOverParam(t *testing.T) {
	env := executor.TaskEnv(
		map[workflow.TaskID]string{"x": "from-output"},
		workflow.ParamValues{"x": "from-param"},
		nil, nil, nil, "", "",
	)
	if !hasEnv(env, "x", "from-output") {
		t.Errorf("expected output to win for x, env=%v", env)
	}
}

// TestShellEnvNeutralizesMetacharacters is the safety property the env scheme
// exists for: an upstream output full of shell metacharacters is inert when a
// command reads it as "$a", where splicing it as {{a}} would execute it.
func TestShellEnvNeutralizesMetacharacters(t *testing.T) {
	// Task a (echo runtime) emits a value that, if substituted into the shell
	// line, would run `whoami` and expand $HOME; read via "$a" it stays literal.
	const payload = "`whoami` $(id) ${HOME}"
	src := `
name: wf
runtime: exec-echo
model: m1
tasks:
  - id: a
    prompt: '` + payload + `'
  - id: b
    depends_on: [a]
    command: printf '%s' "$a"
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["b"]; got != payload {
		t.Errorf("Outputs[b] = %q, want the literal payload %q (metacharacters must not execute)", got, payload)
	}
}
