package executor

import (
	"errors"

	"github.com/mcuste/loom/pkg/runtime"
	"github.com/mcuste/loom/pkg/workflow"
)

// transientClassifier reports whether err is a transient, retryable failure.
// It inspects typed errors first (runtime.ExecError and ShellError stderr) and
// falls back to matching err.Error() for unwrapped errors. Matching the typed
// Stderr field directly avoids depending on incidental Error() formatting.
func transientClassifier(err error) bool {
	if err == nil {
		return false
	}
	var ee *runtime.ExecError
	if errors.As(err, &ee) {
		// Match the captured Stderr, but also the wrapped Err: a transient
		// signal can live in either field (e.g. a "timeout" with empty stderr),
		// and the old err.Error()-based path saw both.
		if transientRe.MatchString(ee.Stderr) {
			return true
		}
		return ee.Err != nil && transientRe.MatchString(ee.Err.Error())
	}
	var se *ShellError
	if errors.As(err, &se) {
		return transientRe.MatchString(se.Stderr)
	}
	return transientRe.MatchString(err.Error())
}

// classifiers maps a retry class to the predicate that recognizes errors of
// that class. retryable consults this registry per class named in Retry.On.
var classifiers = map[workflow.RetryClass]func(error) bool{
	workflow.RetryClassTransient: transientClassifier,
}
