package executor

import (
	"errors"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// TestTransientClassifier pins the typed transient classifier across its three
// inspection paths: a runtime.ExecError's Stderr, a ShellError's Stderr, and
// the plain-string fallback for unwrapped errors, plus a non-transient miss.
// Typed cases assert classification via the typed Stderr field rather than
// incidental string formatting.
func TestTransientClassifier(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			"exec_error_stderr",
			&runtime.ExecError{Name: "codex", Err: errors.New("exit status 1"), Stderr: "429 too many requests"},
			true,
		},
		{
			"shell_error_stderr",
			&ShellError{ExitCode: 1, Stderr: "read tcp: connection reset by peer"},
			true,
		},
		{
			"plain_string_fallback",
			errors.New("503 service unavailable"),
			true,
		},
		{
			"non_transient_miss",
			errors.New("400 invalid request: bad prompt"),
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := transientClassifier(tc.err); got != tc.want {
				t.Errorf("transientClassifier(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassifierRegistry_CoversEveryValidRetryClass is the drift guard between
// the parse vocabulary and the runtime registry: every class the parser accepts
// must have a registered classifier, so parse can never admit a class the
// runtime would silently ignore. It also asserts no classifier is registered
// for a class the parser would reject. The guard iterates the vocabulary rather
// than spot-checking, so a new class with no classifier is caught.
func TestClassifierRegistry_CoversEveryValidRetryClass(t *testing.T) {
	t.Parallel()
	for _, class := range workflow.RetryClasses() {
		if _, ok := classifiers[class]; !ok {
			t.Errorf("no classifier registered for valid class %q", class)
		}
	}
	for class := range classifiers {
		if !workflow.ValidRetryClass(class) {
			t.Errorf("classifier registered for %q but ValidRetryClass rejects it", class)
		}
	}
}
