package executor

import (
	"context"
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

// isTransient reports whether err looks like a transient, retryable failure.
// A nil error is never transient. The match runs case-insensitively over
// err.Error(), so it sees ExecError/ShellError-wrapped stderr the same way.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	return transientRe.MatchString(err.Error())
}

// runWithRetry invokes attempt, then retries it up to t.Retry.Max additional
// times while the returned error is classified as a retryable class in
// t.Retry.On. Between attempts it sleeps per the backoff schedule using base as
// the base delay, honoring ctx cancellation during the wait. With retry
// disabled (or a non-retryable error) it behaves exactly like a single attempt.
// The returned TaskResult reflects the last attempt.
func runWithRetry(ctx context.Context, t *workflow.Task, base time.Duration, attempt func() (TaskResult, error)) (TaskResult, error) {
	res, err := attempt()
	if err == nil || !t.Retry.Enabled() {
		return res, err
	}
	for i := 1; i <= t.Retry.Max; i++ {
		if !retryable(t.Retry, err) {
			return res, err
		}
		if waitErr := sleepBackoff(ctx, t.Retry.Backoff, base, i); waitErr != nil {
			return res, waitErr
		}
		res, err = attempt()
		if err == nil {
			return res, nil
		}
	}
	return res, err
}

// retryable reports whether err belongs to a class the policy opts into. Only
// the "transient" class is recognized; an error qualifies when "transient" is
// listed in r.On AND isTransient classifies it as such.
func retryable(r workflow.Retry, err error) bool {
	for _, class := range r.On {
		if class == "transient" && isTransient(err) {
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
