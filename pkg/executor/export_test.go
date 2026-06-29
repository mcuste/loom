package executor

import "github.com/mcuste/loom/pkg/workflow"

// This file exposes internals to the external executor_test package. It is a
// test-only build (the _test.go suffix), so these accessors never widen the
// permanent public API.

// HasClassifier reports whether class has a registered classifier. It is the
// drift guard between the parser's retry-class vocabulary and the runtime
// registry: every class the parser admits must be recognized at runtime.
func HasClassifier(class workflow.RetryClass) bool {
	_, ok := classifiers[class]
	return ok
}

// ClassifierClasses returns every retry class the runtime can recognize. It
// lets the drift guard assert the reverse direction: every recognized class
// must be one the parser admits.
func ClassifierClasses() []workflow.RetryClass {
	classes := make([]workflow.RetryClass, 0, len(classifiers))
	for class := range classifiers {
		classes = append(classes, class)
	}
	return classes
}

// TaskEnv exposes taskEnv to the external test package so the bare-name env
// injection scheme (naming, precedence, leading-digit skip) can be asserted
// without re-deriving it.
func TaskEnv(
	outputs map[workflow.TaskID]string,
	params workflow.ParamValues,
	state map[string]string,
	prev map[workflow.TaskID]string,
	exitCodes map[workflow.TaskID]int,
	loopVar, loopVal string,
) []string {
	return taskEnv(outputs, params, state, prev, exitCodes, loopVar, loopVal)
}
