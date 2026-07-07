package workflow

import (
	"fmt"
	"math"

	"github.com/mcuste/loom/pkg/syntax"
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

// MalformedBudgetError reports a structurally invalid `budget:` block: the node
// was not a mapping, or a key inside it was not a scalar. It is typed (rather
// than a bare errors.New) so callers can errors.As it apart from
// InvalidBudgetError and UnknownBudgetFieldError.
type MalformedBudgetError struct{ Reason string }

func (e *MalformedBudgetError) Error() string {
	return "budget: " + e.Reason
}

// parseBudget decodes a `budget:` mapping (workflow- or task-level) into a
// *Budget. An absent block (zero-value node) yields nil: no limit. A present
// block requires `max_cost_usd` to be a positive float; zero, negative, or
// absent is rejected with InvalidBudgetError.
func parseBudget(node syntax.Value) (*Budget, error) {
	if !node.Present() {
		return nil, nil
	}
	if node.Kind() != syntax.MappingNode {
		return nil, &MalformedBudgetError{Reason: "must be a mapping"}
	}
	b := &Budget{}
	if err := node.EachMapEntry("budget:", func(key string, v syntax.Value) error {
		switch key {
		case "max_cost_usd":
			if err := v.Decode(&b.MaxCostUSD); err != nil {
				return fmt.Errorf("budget.max_cost_usd: %w", err)
			}
		default:
			return &UnknownBudgetFieldError{Field: key}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Reject anything that is not a finite positive float. The `<= 0` form would
	// admit NaN (NaN <= 0 is false in IEEE 754), which silently disables
	// enforcement downstream since `spent > NaN` is always false; `!(x > 0)`
	// rejects NaN, zero, and negatives, and the IsInf guard rejects +Inf.
	if !(b.MaxCostUSD > 0) || math.IsInf(b.MaxCostUSD, 1) {
		return nil, &InvalidBudgetError{Value: b.MaxCostUSD}
	}
	return b, nil
}
