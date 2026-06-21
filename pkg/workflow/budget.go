package workflow

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Budget caps cumulative cost in US dollars. At the workflow level it limits
// total spend across all completed tasks; at the task level it caps the cost
// of a single task's retries. A nil *Budget means "no limit".
type Budget struct {
	// MaxCostUSD is the spend ceiling in US dollars. Must be a positive float.
	MaxCostUSD float64
}

// InvalidBudgetError reports a `budget:` block whose max_cost_usd is not a
// positive float (it was zero, negative, or absent).
type InvalidBudgetError struct {
	Value float64
}

func (e *InvalidBudgetError) Error() string {
	return fmt.Sprintf("invalid budget max_cost_usd %v: must be a positive float", e.Value)
}

// UnknownBudgetFieldError reports a key inside a `budget:` mapping that is not
// `max_cost_usd`.
type UnknownBudgetFieldError struct{ Field string }

func (e *UnknownBudgetFieldError) Error() string {
	return fmt.Sprintf("budget: unknown field %q", e.Field)
}

// parseBudget decodes a `budget:` mapping (workflow- or task-level) into a
// *Budget. An absent block (zero-value node) yields nil — no limit. A present
// block requires `max_cost_usd` to be a positive float; zero, negative, or
// absent is rejected with InvalidBudgetError.
func parseBudget(node yaml.Node) (*Budget, error) {
	if node.Kind == 0 {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("budget: must be a mapping")
	}
	b := &Budget{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return nil, errors.New("budget: key must be a scalar")
		}
		switch k.Value {
		case "max_cost_usd":
			if err := v.Decode(&b.MaxCostUSD); err != nil {
				return nil, fmt.Errorf("budget.max_cost_usd: %w", err)
			}
		default:
			return nil, &UnknownBudgetFieldError{Field: k.Value}
		}
	}
	if b.MaxCostUSD <= 0 {
		return nil, &InvalidBudgetError{Value: b.MaxCostUSD}
	}
	return b, nil
}
