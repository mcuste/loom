package executor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestRun_RetriesTypedTransientErrors exercises the transient classifier
// through the public retry path rather than calling the unexported predicate.
// A runtime that fails once with a typed error carrying a transient signal in
// its Stderr must be retried (2 attempts); a non-transient signal must not
// (1 attempt). This proves the classifier inspects the typed Stderr field
// rather than incidental Error() formatting.
func TestRun_RetriesTypedTransientErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		err       error
		wantCalls int
	}{
		{
			"exec_error_stderr_is_transient",
			&runtime.ExecError{Name: "codex", Err: errors.New("exit status 1"), Stderr: "429 too many requests"},
			2,
		},
		{
			"shell_error_stderr_is_transient",
			&executor.ShellError{ExitCode: 1, Stderr: "read tcp: connection reset by peer"},
			2,
		},
		{
			"non_transient_is_not_retried",
			&runtime.ExecError{Name: "codex", Err: errors.New("exit status 1"), Stderr: "400 invalid request: bad prompt"},
			1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt, flaky := newFlaky(t, 1, tc.err)
			src := `
name: wf
runtime: ` + rt + `
model: m1
tasks:
  - id: a
    prompt: hello
    retry:
      max: 2
      backoff: none
      on: [transient]
`
			wf, err := workflow.Parse([]byte(src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			_, _ = executor.Run(context.Background(), wf, executor.Hooks{}, executor.Options{RetryBaseDelay: fastBackoff})
			if got := flaky.callCount(); got != tc.wantCalls {
				t.Errorf("attempts = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}
