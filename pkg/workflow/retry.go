package workflow

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// parseRetry decodes a task's retry: mapping into a Retry policy.
func parseRetry(tid TaskID, node yaml.Node) (Retry, error) {
	if node.Kind == 0 {
		return Retry{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return Retry{}, fmt.Errorf("task %q: retry must be a mapping", tid)
	}
	r := Retry{Backoff: BackoffExponential, On: []string{string(RetryClassTransient)}}
	if err := eachMapEntry(&node, fmt.Sprintf("task %q: retry", tid), func(key string, v *yaml.Node) error {
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
