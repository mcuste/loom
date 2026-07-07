package workflow

import (
	"fmt"

	"github.com/mcuste/loom/pkg/syntax"
)

// parseRetry decodes a task's retry: mapping into a Retry policy.
func parseRetry(tid TaskID, node syntax.Value) (Retry, error) {
	if !node.Present() {
		return Retry{}, nil
	}
	if node.Kind() != syntax.MappingNode {
		return Retry{}, fmt.Errorf("task %q: retry must be a mapping", tid)
	}
	r := Retry{Backoff: BackoffExponential, On: []string{string(RetryClassTransient)}}
	if err := node.EachMapEntry(fmt.Sprintf("task %q: retry", tid), func(key string, v syntax.Value) error {
		switch key {
		case "max":
			if err := v.Decode(&r.Max); err != nil {
				return fmt.Errorf("task %q: retry.max: %w", tid, err)
			}
			if r.Max < 0 {
				return &InvalidRetryMaxError{Task: tid, Max: r.Max}
			}
		case "backoff":
			var b string
			if err := v.Decode(&b); err != nil {
				return fmt.Errorf("task %q: retry.backoff: %w", tid, err)
			}
			switch Backoff(b) {
			case BackoffNone, BackoffConstant, BackoffExponential:
				r.Backoff = Backoff(b)
			default:
				return &UnknownBackoffError{Task: tid, Backoff: b}
			}
		case "on":
			var on []string
			if err := v.Decode(&on); err != nil {
				return fmt.Errorf("task %q: retry.on: %w", tid, err)
			}
			for _, c := range on {
				if !ValidRetryClass(RetryClass(c)) {
					return &UnknownRetryClassError{Task: tid, Class: c}
				}
			}
			r.On = on
		default:
			return &UnknownRetryFieldError{Task: tid, Field: key}
		}
		return nil
	}); err != nil {
		return Retry{}, err
	}
	return r, nil
}
