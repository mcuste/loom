package executor_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// writeScript drops an executable script into a temp dir and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// TestScriptHappyPath verifies a script task captures stdout as Output, exits 0,
// and reports zero Usage.
func TestScriptHappyPath(t *testing.T) {
	path := writeScript(t, "#!/bin/sh\necho hi\n")
	wf := &workflow.Workflow{
		ID:    "wf",
		Tasks: []workflow.Task{{ID: "a", Script: path}},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["a"]; got != "hi" {
		t.Errorf("Outputs[a] = %q, want %q", got, "hi")
	}
	if got := rep.Tasks[0].ExitCode; got != 0 {
		t.Errorf("ExitCode = %d, want 0", got)
	}
	if (rep.Tasks[0].Usage != runtime.Usage{}) {
		t.Errorf("Usage = %+v, want zero", rep.Tasks[0].Usage)
	}
}

// TestScriptNonZeroExitIsData verifies a non-zero exit does NOT fail the task:
// the code is captured in ExitCode, stdout is still captured, and the run
// succeeds so downstream tasks proceed.
func TestScriptNonZeroExitIsData(t *testing.T) {
	path := writeScript(t, "#!/bin/sh\necho partial\nexit 3\n")
	wf := &workflow.Workflow{
		ID:    "wf",
		Tasks: []workflow.Task{{ID: "a", Script: path}},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run returned error for non-zero exit, want success: %v", err)
	}
	if got := rep.Tasks[0].ExitCode; got != 3 {
		t.Errorf("ExitCode = %d, want 3", got)
	}
	if got := rep.Outputs["a"]; got != "partial" {
		t.Errorf("Outputs[a] = %q, want %q", got, "partial")
	}
	if rep.Tasks[0].Status != executor.StatusOK {
		t.Errorf("Status = %v, want StatusOK", rep.Tasks[0].Status)
	}
}

// TestScriptLaunchFailureErrors verifies that a script which cannot be launched
// (the file does not exist) fails the task, since there is no exit code.
func TestScriptLaunchFailureErrors(t *testing.T) {
	wf := &workflow.Workflow{
		ID:    "wf",
		Tasks: []workflow.Task{{ID: "a", Script: filepath.Join(t.TempDir(), "nope.sh")}},
	}
	_, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err == nil {
		t.Fatal("Run returned nil error for a missing script, want failure")
	}
}

// TestScriptArgsSubstituted verifies argv entries are passed to the script and
// carry placeholder substitution.
func TestScriptArgsSubstituted(t *testing.T) {
	// Echoes its two args separated by a dash.
	path := writeScript(t, "#!/bin/sh\nprintf '%s-%s' \"$1\" \"$2\"\n")
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "up", Command: "echo world"},
			{ID: "a", Script: path, Args: []string{"{{params.greeting}}", "{{up}}"}, DependsOn: []workflow.TaskID{"up"}},
		},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{
		Params: workflow.ParamValues{"greeting": "hello"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["a"]; got != "hello-world" {
		t.Errorf("Outputs[a] = %q, want %q", got, "hello-world")
	}
}

// TestScriptExitPlaceholderDownstream verifies `{{id.exit}}` substitutes a
// script's exit code into a downstream task body.
func TestScriptExitPlaceholderDownstream(t *testing.T) {
	path := writeScript(t, "#!/bin/sh\nexit 7\n")
	wf := &workflow.Workflow{
		ID: "wf",
		Tasks: []workflow.Task{
			{ID: "check", Script: path},
			{ID: "report", Command: "echo code={{check.exit}}", DependsOn: []workflow.TaskID{"check"}},
		},
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["report"]; got != "code=7" {
		t.Errorf("Outputs[report] = %q, want %q", got, "code=7")
	}
}

// TestScriptContextCancel verifies that cancelling the run while a script is in
// flight fails the task (a killed child yields a -1 "exit code" that is NOT
// recorded as branchable data), within a short deadline.
func TestScriptContextCancel(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	path := writeScript(t, "#!/bin/sh\ntouch "+marker+"\nexec sleep 60\n")
	wf := &workflow.Workflow{
		ID:    "wf",
		Tasks: []workflow.Task{{ID: "a", Script: path}},
	}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan *executor.Report, 1)
	errc := make(chan error, 1)
	go func() {
		rep, err := executor.Run(ctx, wf, executor.Hooks{}, executor.Options{})
		done <- rep
		errc <- err
	}()

	startDeadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-startDeadline:
			t.Fatal("subprocess did not start within 2s")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Run returned nil error after context cancel; a killed script must fail, not succeed with exit -1")
		}
		rep := <-done
		for _, r := range rep.Tasks {
			if r.TaskID == "a" && r.Status == executor.StatusOK {
				t.Errorf("cancelled script recorded StatusOK with exit=%d, want it to fail", r.ExitCode)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after context cancel")
	}
}

// TestScriptSeedExitCodeRestored pins that a seeded script task restores its
// exit code into the run, so a downstream `{{id.exit}}` resolves to the recorded
// value. The code==0 case is the regression guard: an absent entry leaves the
// placeholder verbatim, so a seeded clean exit must record an explicit 0.
func TestScriptSeedExitCodeRestored(t *testing.T) {
	for _, code := range []int{0, 5} {
		t.Run(fmt.Sprintf("code=%d", code), func(t *testing.T) {
			wf := &workflow.Workflow{
				ID: "wf",
				Tasks: []workflow.Task{
					{ID: "check", Script: "/does/not/run/when/seeded"},
					{ID: "report", Command: "echo code={{check.exit}}", DependsOn: []workflow.TaskID{"check"}},
				},
			}
			rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{
				Seed:          map[workflow.TaskID]string{"check": "stdout"},
				SeedExitCodes: map[workflow.TaskID]int{"check": code},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got, want := rep.Outputs["report"], fmt.Sprintf("code=%d", code); got != want {
				t.Errorf("Outputs[report] = %q, want %q", got, want)
			}
		})
	}
}

// TestScriptInForEachLoop pins that a script member of a for_each loop captures
// its per-iteration exit code, and the final iteration's code is exposed to a
// downstream `{{member.exit}}` consumer through the loop merge-back.
func TestScriptInForEachLoop(t *testing.T) {
	path := writeScript(t, "#!/bin/sh\nexit \"$1\"\n")
	src := fmt.Sprintf(`
name: wf
tasks:
  - id: probe
    for_each:
      in: ["1", "2", "3"]
      as: code
      tasks:
        - id: run
          script: %s
          args: ["{{code}}"]
  - id: final
    depends_on: [run]
    command: "echo final={{run.exit}}"
`, path)
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rep.Outputs["final"]; got != "final=3" {
		t.Errorf("Outputs[final] = %q, want %q (last iteration's exit code)", got, "final=3")
	}
}

// TestScriptExitWhenGate verifies a `when: {{id.exit}} != 0` guard runs a
// downstream task only on a non-zero exit, and skips it on a clean exit.
func TestScriptExitWhenGate(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus string
	}{
		{"nonzero runs", "#!/bin/sh\nexit 1\n", "ran"},
		{"zero skips", "#!/bin/sh\nexit 0\n", "skipped"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeScript(t, tc.body)
			cond, err := workflow.ParseCondition("{{check.exit}} != 0", map[workflow.TaskID]bool{"check": true})
			if err != nil {
				t.Fatalf("ParseCondition: %v", err)
			}
			wf := &workflow.Workflow{
				ID: "wf",
				Tasks: []workflow.Task{
					{ID: "check", Script: path},
					{ID: "alert", Command: "echo ran", When: "{{check.exit}} != 0", Cond: cond, DependsOn: []workflow.TaskID{"check"}},
				},
			}
			rep, err := executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			var alert executor.TaskResult
			for _, r := range rep.Tasks {
				if r.TaskID == "alert" {
					alert = r
				}
			}
			if tc.wantStatus == "ran" {
				if alert.Status != executor.StatusOK || rep.Outputs["alert"] != "ran" {
					t.Errorf("alert status=%v output=%q, want it to run", alert.Status, rep.Outputs["alert"])
				}
			} else {
				if alert.Status != executor.StatusSkipped {
					t.Errorf("alert status=%v, want StatusSkipped", alert.Status)
				}
			}
		})
	}
}
