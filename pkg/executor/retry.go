package executor

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/mcuste/loom/pkg/workflow"
)

// defaultRetryBaseDelay is the base backoff delay used when Options.RetryBaseDelay
// is zero. constant backoff waits this each retry; exponential backoff waits
// base * 2^(attempt-1). Tests inject a tiny value via Options to avoid real
// multi-second sleeps, so there is no mutable package-level state to race on.
const defaultRetryBaseDelay = time.Second

// maxBackoffShift caps the exponent used by exponential backoff so the
// time.Duration shift cannot overflow into a zero or negative delay at very
// large attempt counts.
const maxBackoffShift = 30

// transientRe matches error messages that signal a transient, retryable
// failure: HTTP 5xx (500/502/503/504), rate limiting (429 / "rate limit" /
// "too many requests"), "overloaded", timeouts, and connection
// resets/refusals/EOF/"temporarily unavailable". Matched case-insensitively.
var transientRe = regexp.MustCompile(`(?i)(50[0234]\b|\b429\b|rate limit|too many requests|overloaded|timeout|timed out|connection reset|connection refused|\beof\b|temporarily unavailable)`)

// runWithRetry invokes attempt, then retries it up to t.Retry.Max additional
// times while the returned error is classified as a retryable class in
// t.Retry.On. Between attempts it sleeps per the backoff schedule using base as
// the base delay, honoring ctx cancellation during the wait. With retry
// disabled (or a non-retryable error) it behaves exactly like a single attempt.
// The returned TaskResult reflects the last attempt.
//
// A per-task budget (t.Budget) caps the retries: the cost of each attempt is
// accumulated, and once the spend strictly exceeds the limit no further retry
// is attempted. The cap returns a typed *BudgetExceededError (carrying the
// limit and the spend so far) so callers can distinguish a budget-capped task
// from one that exhausted its retries via errors.As. Spend landing exactly on
// the limit still permits the next retry, mirroring the workflow-level rule.
func runWithRetry(ctx context.Context, t *workflow.Task, base time.Duration, attempt func() (TaskResult, error)) (TaskResult, error) {
	res, err := attempt()
	spent := res.Usage.TotalCostUSD
	if err == nil || !t.Retry.Enabled() {
		return res, err
	}
	for i := 1; i <= t.Retry.Max; i++ {
		if !retryable(t.Retry, err) {
			return res, err
		}
		if t.Budget != nil && spent > t.Budget.MaxCostUSD {
			return res, &BudgetExceededError{Limit: t.Budget.MaxCostUSD, Spent: spent}
		}
		if waitErr := sleepBackoff(ctx, t.Retry.Backoff, base, i); waitErr != nil {
			return res, waitErr
		}
		res, err = attempt()
		spent += res.Usage.TotalCostUSD
		if err == nil {
			return res, nil
		}
	}
	return res, err
}

// retryable reports whether err belongs to a class the policy opts into. Each
// class name in r.On is looked up in the classifier registry; err qualifies if
// any registered classifier matches it. Class names with no classifier (none
// can be admitted by the parser, per the drift guard) are ignored.
func retryable(r workflow.Retry, err error) bool {
	// A schema mismatch is retryable whenever the policy is enabled, regardless
	// of its `on:` classes: the model can produce a conforming output on a fresh
	// attempt, and the task wording ties the retry to "a retry policy is set".
	var schemaErr *SchemaError
	if errors.As(err, &schemaErr) {
		return true
	}
	for _, class := range r.On {
		classify, ok := classifiers[workflow.RetryClass(class)]
		if ok && classify(err) {
			return true
		}
	}
	return false
}

// sleepBackoff waits the delay for the given attempt (1-based) under the named
// schedule, returning ctx.Err() if ctx is cancelled before the delay elapses.
// A non-positive delay returns immediately.
func sleepBackoff(ctx context.Context, b workflow.Backoff, base time.Duration, attempt int) error {
	d := backoffDelay(b, base, attempt)
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backoffDelay computes the wait before the given attempt (1-based): none → 0,
// constant → base, exponential → base * 2^(attempt-1). The exponent is capped
// at maxBackoffShift so the shift cannot overflow the duration.
func backoffDelay(b workflow.Backoff, base time.Duration, attempt int) time.Duration {
	switch b {
	case workflow.BackoffConstant:
		return base
	case workflow.BackoffExponential:
		return base << min(attempt-1, maxBackoffShift)
	default:
		return 0
	}
}
