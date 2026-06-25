package runtime_test

import (
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

// TestUsageAdd_AccumulatesEveryField pins that Add folds another Usage into the
// receiver field-by-field, replacing the hand-reinvented 4-field accumulation
// scattered across the executor and tui consumers.
func TestUsageAdd_AccumulatesEveryField(t *testing.T) {
	t.Parallel()
	sut := runtime.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, TotalCostUSD: 0.50}

	sut.Add(runtime.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, TotalCostUSD: 0.25})

	want := runtime.Usage{InputTokens: 11, OutputTokens: 22, CacheReadTokens: 8, TotalCostUSD: 0.75}
	if sut != want {
		t.Errorf("Add did not accumulate every field: got %+v, want %+v", sut, want)
	}
}
