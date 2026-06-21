package tui

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/muesli/termenv"

	"github.com/mcuste/loom/pkg/executor"
)

// TestRenderBadge_PerStateBadge pins the distinct badge rendered for each task
// disposition: a cache hit, a when-skip, a resume seed, an in-flight retry, and
// a failure (whose error is shown inline). Each subtest constructs the realistic
// signal for its state and asserts the badge surfaces that state.
func TestRenderBadge_PerStateBadge(t *testing.T) {
	sym := symbolsFor(termenv.TrueColor)
	cases := []struct {
		name     string
		res      executor.TaskResult
		err      error
		seeded   bool
		retrying bool
		want     string
	}{
		{
			name: "cache",
			res:  executor.TaskResult{Status: executor.StatusOK, CacheHit: true},
			want: sym.cacheHit + " cache",
		},
		{
			name: "skip",
			res:  executor.TaskResult{Status: executor.StatusSkipped},
			want: sym.skipped + " skip",
		},
		{
			name:   "seed",
			res:    executor.TaskResult{Status: executor.StatusOK},
			seeded: true,
			want:   sym.seeded + " seed",
		},
		{
			name:     "retry",
			res:      executor.TaskResult{},
			retrying: true,
			want:     sym.retry + " retry",
		},
		{
			name: "fail",
			res:  executor.TaskResult{Status: executor.StatusOK},
			err:  errors.New("boom"),
			// Assert the leading failure glyph too, so a right message under
			// the wrong prefix cannot pass.
			want: sym.failed + " fail: boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := renderBadge(badgeState{res: tc.res, err: tc.err, seeded: tc.seeded, retrying: tc.retrying}, sym)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("renderBadge %s = %q, want it to contain %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestBudgetGauge_ReportsSpendPercentage pins the budget gauge at three points:
// no spend (0%), partial spend (50%), and over the ceiling (200%), so the
// status bar surfaces how much of the workflow budget is consumed.
func TestBudgetGauge_ReportsSpendPercentage(t *testing.T) {
	sym := symbolsFor(termenv.TrueColor)
	cases := []struct {
		name  string
		spent float64
		max   float64
		want  string
	}{
		{name: "zero", spent: 0, max: 1, want: "0%"},
		{name: "partial", spent: 0.5, max: 1, want: "50%"},
		{name: "over", spent: 2, max: 1, want: "200%"},
		// A non-positive max has no gauge to draw; the guard returns the empty
		// sentinel rather than dividing by zero.
		{name: "zero-max", spent: 1, max: 0, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := budgetGauge(tc.spent, tc.max, sym)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("budgetGauge(%v, %v) = %q, want empty", tc.spent, tc.max, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("budgetGauge(%v, %v) = %q, want it to contain %q", tc.spent, tc.max, got, tc.want)
			}
		})
	}
}

// TestSymbols_ASCIIFallbackEmitsOnlyASCII pins that the ASCII fallback profile
// renders every badge and the gauge with ASCII-only runes, so terminals without
// Unicode still get legible (non-empty) output.
func TestSymbols_ASCIIFallbackEmitsOnlyASCII(t *testing.T) {
	sym := symbolsFor(termenv.Ascii)

	var b strings.Builder
	b.WriteString(renderBadge(badgeState{res: executor.TaskResult{Status: executor.StatusOK, CacheHit: true}}, sym))
	b.WriteString(renderBadge(badgeState{res: executor.TaskResult{Status: executor.StatusSkipped}}, sym))
	b.WriteString(renderBadge(badgeState{res: executor.TaskResult{Status: executor.StatusOK}, seeded: true}, sym))
	b.WriteString(renderBadge(badgeState{retrying: true}, sym))
	b.WriteString(renderBadge(badgeState{res: executor.TaskResult{Status: executor.StatusOK}, err: errors.New("boom")}, sym))
	b.WriteString(budgetGauge(0.5, 1, sym))
	out := b.String()

	if out == "" {
		t.Fatal("ASCII fallback rendered nothing; expected legible badges and gauge")
	}
	for i, r := range out {
		if r >= utf8.RuneSelf {
			t.Fatalf("non-ASCII rune %q at byte %d in ASCII fallback output %q", r, i, out)
		}
	}
}

// TestLoopRibbon_ShowsIterationOverMax pins that the loop ribbon surfaces the
// current iteration over the maximum from RunMeta's loop metadata.
func TestLoopRibbon_ShowsIterationOverMax(t *testing.T) {
	got := loopRibbon(RunMeta{Loop: &LoopMeta{N: 2, Max: 5}})
	if !strings.Contains(got, "2/5") {
		t.Fatalf("loopRibbon = %q, want it to show iteration 2/5", got)
	}
}
