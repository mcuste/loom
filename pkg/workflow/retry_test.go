package workflow_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/mcuste/loom/pkg/workflow"
)

// TestRetryEnabled_FalseForZeroValue pins that the zero-value policy (no retry
// block) reports disabled.
func TestRetryEnabled_FalseForZeroValue(t *testing.T) {
	var r workflow.Retry
	if r.Enabled() {
		t.Errorf("Retry{}.Enabled() = true, want false")
	}
}

// TestRetryEnabled_TrueWhenMaxPositive pins that a positive Max enables retry.
func TestRetryEnabled_TrueWhenMaxPositive(t *testing.T) {
	r := workflow.Retry{Max: 1}
	if !r.Enabled() {
		t.Errorf("Retry{Max:1}.Enabled() = false, want true")
	}
}

// retrySrc builds a single-LLM-task workflow whose task carries the given
// retry: block body (already indented under `retry:`).
func retrySrc(retryBlock string) string {
	return `
name: wf
runtime: test-rt
model: m1
tasks:
  - id: a
    prompt: hello
` + retryBlock
}

// TestParse_RetryBlockPopulatesTask checks a fully specified retry block lands
// on Task.Retry verbatim.
func TestParse_RetryBlockPopulatesTask(t *testing.T) {
	wf, err := workflow.Parse([]byte(retrySrc(`    retry:
      max: 3
      backoff: constant
      on: [transient]
`)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.Tasks[0].Retry
	if got.Max != 3 {
		t.Errorf("Retry.Max = %d, want 3", got.Max)
	}
	if got.Backoff != workflow.BackoffConstant {
		t.Errorf("Retry.Backoff = %q, want %q", got.Backoff, workflow.BackoffConstant)
	}
	if !slices.Equal(got.On, []string{"transient"}) {
		t.Errorf("Retry.On = %v, want [transient]", got.On)
	}
}

// TestParse_RetryDefaultsBackoffExponential checks backoff defaults to
// exponential when the retry block is present but omits backoff.
func TestParse_RetryDefaultsBackoffExponential(t *testing.T) {
	wf, err := workflow.Parse([]byte(retrySrc(`    retry:
      max: 2
`)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := wf.Tasks[0].Retry.Backoff; got != workflow.BackoffExponential {
		t.Errorf("Retry.Backoff = %q, want %q (default)", got, workflow.BackoffExponential)
	}
}

// TestParse_RetryDefaultsOnTransient checks `on` defaults to [transient] when
// the retry block is present but omits it.
func TestParse_RetryDefaultsOnTransient(t *testing.T) {
	wf, err := workflow.Parse([]byte(retrySrc(`    retry:
      max: 2
`)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := wf.Tasks[0].Retry.On; !slices.Equal(got, []string{"transient"}) {
		t.Errorf("Retry.On = %v, want [transient] (default)", got)
	}
}

// TestParse_NoRetryBlockLeavesZeroValue checks a task without a retry block has
// a disabled zero-value policy.
func TestParse_NoRetryBlockLeavesZeroValue(t *testing.T) {
	wf, err := workflow.Parse([]byte(retrySrc("")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := wf.Tasks[0].Retry; got.Max != 0 || got.Enabled() {
		t.Errorf("Retry = %+v, want zero value (disabled)", got)
	}
}

// TestParse_RetryExplicitMaxZeroIsDisabled checks an explicit `retry: max: 0`
// block parses with Max 0 (so Enabled() is false), even though backoff and on
// are defaulted to non-zero values. The disabled invariant rides on Max, not
// on the struct being entirely zero.
func TestParse_RetryExplicitMaxZeroIsDisabled(t *testing.T) {
	wf, err := workflow.Parse([]byte(retrySrc(`    retry:
      max: 0
`)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.Tasks[0].Retry
	if got.Max != 0 {
		t.Errorf("Retry.Max = %d, want 0", got.Max)
	}
	if got.Enabled() {
		t.Errorf("Retry.Enabled() = true, want false for explicit max 0")
	}
}

// TestParse_RetryEmptyOnList checks an explicit empty `on: []` (with a positive
// max) parses to an empty On slice. No class is retryable, so retries are
// silently inert; this pins that the parser preserves the empty list rather
// than re-applying the [transient] default.
func TestParse_RetryEmptyOnList(t *testing.T) {
	wf, err := workflow.Parse([]byte(retrySrc(`    retry:
      max: 3
      on: []
`)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.Tasks[0].Retry
	if got.Max != 3 {
		t.Errorf("Retry.Max = %d, want 3", got.Max)
	}
	if len(got.On) != 0 {
		t.Errorf("Retry.On = %v, want empty (no class retryable)", got.On)
	}
}

// TestParse_RejectsNegativeRetryMax checks a negative max is a typed error.
func TestParse_RejectsNegativeRetryMax(t *testing.T) {
	_, err := workflow.Parse([]byte(retrySrc(`    retry:
      max: -1
`)))
	var want *workflow.InvalidRetryMaxError
	if !errors.As(err, &want) {
		t.Fatalf("errors.As InvalidRetryMaxError failed; err = %v", err)
	}
}

// TestParse_RejectsUnknownBackoff checks an out-of-enum backoff is a typed error.
func TestParse_RejectsUnknownBackoff(t *testing.T) {
	_, err := workflow.Parse([]byte(retrySrc(`    retry:
      max: 2
      backoff: linear
`)))
	var want *workflow.UnknownBackoffError
	if !errors.As(err, &want) {
		t.Fatalf("errors.As UnknownBackoffError failed; err = %v", err)
	}
}

// TestParse_RejectsUnknownRetryClass checks an unrecognized `on` class is a
// typed error.
func TestParse_RejectsUnknownRetryClass(t *testing.T) {
	_, err := workflow.Parse([]byte(retrySrc(`    retry:
      max: 2
      on: [permanent]
`)))
	var want *workflow.UnknownRetryClassError
	if !errors.As(err, &want) {
		t.Fatalf("errors.As UnknownRetryClassError failed; err = %v", err)
	}
}

// TestParse_RetryAllowedOnShellTask checks retry is permitted on a shell task
// (unlike runtime/model/effort), and the block is parsed.
func TestParse_RetryAllowedOnShellTask(t *testing.T) {
	src := `
name: wf
tasks:
  - id: a
    command: echo hi
    retry:
      max: 2
      backoff: none
`
	wf, err := workflow.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := wf.Tasks[0].Retry
	if got.Max != 2 || got.Backoff != workflow.BackoffNone {
		t.Errorf("Retry = %+v, want Max 2 / backoff none", got)
	}
}
