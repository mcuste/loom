package executor

import "fmt"

// BudgetExceededError reports that the workflow's cost budget would be exceeded
// by dispatching another task. The executor checks the budget BEFORE
// dispatching, so Spent reflects the cumulative cost of the tasks that already
// completed and Limit is the configured ceiling.
type BudgetExceededError struct {
	Limit float64
	Spent float64
}

// Error formats the configured limit and the accumulated spend that exceeded it.
func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("cost budget exceeded: spent %g of %g limit", e.Spent, e.Limit)
}
