package workflow

import (
	"slices"
	"strconv"
	"strings"
)

// RetryClass names a class of retryable error. Class names are the values
// carried in Retry.On; this type is the single source of truth for the retry
// vocabulary shared with the executor's classifier registry.
type RetryClass string

// RetryClassTransient is the class of genuinely transient, retryable failures
// (HTTP 5xx, rate limiting, timeouts, connection resets, and similar).
const RetryClassTransient RetryClass = "transient"

// RetryClasses returns the recognized retry-class vocabulary in canonical
// order. It is the single source of truth the parser validates against and the
// executor's classifier registry is drift-checked against. A fresh slice is
// returned on every call so no caller can mutate the shared vocabulary.
func RetryClasses() []RetryClass {
	return []RetryClass{RetryClassTransient}
}

// ValidRetryClass reports whether class names a known retry class. It is the
// predicate parse uses to validate retry.on entries.
func ValidRetryClass(class RetryClass) bool {
	return slices.Contains(RetryClasses(), class)
}

// recognizedRetryClasses renders the known retry-class names as a quoted,
// comma-separated phrase for user-facing error messages (e.g. `"transient"`).
// Sourcing it from RetryClasses keeps UnknownRetryClassError from drifting as
// the vocabulary grows.
func recognizedRetryClasses() string {
	classes := RetryClasses()
	quoted := make([]string, len(classes))
	for i, c := range classes {
		quoted[i] = strconv.Quote(string(c))
	}
	return strings.Join(quoted, ", ")
}
