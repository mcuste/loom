package executor

import (
	"errors"
	"testing"
)

// TestIsTransient_ClassifiesRetryableSignals pins which error messages the
// classifier treats as transient (case-insensitive). Each subtest is one
// scenario; the assertion target is the boolean classification.
func TestIsTransient_ClassifiesRetryableSignals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"http_500", "server returned HTTP 500", true},
		{"http_502", "502 Bad Gateway", true},
		{"http_503", "503 Service Unavailable", true},
		{"http_504", "504 Gateway Timeout", true},
		{"rate_limit", "Rate limit exceeded", true},
		{"too_many_requests", "429 too many requests", true},
		{"overloaded", "the model is Overloaded", true},
		{"timeout", "request timeout", true},
		{"timed_out", "context deadline: operation timed out", true},
		{"connection_reset", "read tcp: connection reset by peer", true},
		{"connection_refused", "dial tcp: connection refused", true},
		{"eof", "unexpected EOF", true},
		{"temporarily_unavailable", "service temporarily unavailable", true},
		{"bad_request_400", "400 invalid request: bad prompt", false},
		{"auth_401", "401 unauthorized: invalid api key", false},
		{"plain_failure", "command failed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isTransient(errors.New(tc.msg)); got != tc.want {
				t.Errorf("isTransient(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// TestIsTransient_NilErrorIsNotTransient pins that a nil error is never
// classified transient.
func TestIsTransient_NilErrorIsNotTransient(t *testing.T) {
	t.Parallel()
	if isTransient(nil) {
		t.Errorf("isTransient(nil) = true, want false")
	}
}
