package workflow

import "github.com/mcuste/loom/pkg/runtime"

// Effective returns the runtime, model, and effort the registered runtime will
// see for t. Task-level fields win when non-empty, falling back to the
// workflow-level defaults. SystemPrompt has no task-level override and is
// taken from Workflow.SystemPrompt directly.
func (w *Workflow) Effective(t Task) (runtime.Name, runtime.Model, runtime.Effort) {
	r := t.Runtime
	if r == "" {
		r = w.Runtime
	}
	m := t.Model
	if m == "" {
		m = w.Model
	}
	e := t.Effort
	if e == "" {
		e = w.Effort
	}
	return r, m, e
}

// Substitute replaces every `{{id}}` placeholder in prompt with outputs[id].
// For a workflow obtained from Parse, every placeholder name is guaranteed to
// be present in the task's depends_on, so callers that have already executed
// those dependencies can supply complete outputs. A placeholder whose name is
// missing from outputs is left in place.
func Substitute(prompt string, outputs map[TaskID]string) string {
	return placeholderRe.ReplaceAllStringFunc(prompt, func(match string) string {
		name := TaskID(match[2 : len(match)-2])
		if v, ok := outputs[name]; ok {
			return v
		}
		return match
	})
}
