package tui_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/muesli/termenv"

	"github.com/mcuste/loom/pkg/executor"
	"github.com/mcuste/loom/pkg/tui"
)

// TestRenderBadge_PerStateBadge pins the distinct badge rendered for each task
// disposition: a cache hit, a when-skip, a resume seed, an in-flight retry, and
// a failure (whose error is shown inline). Each subtest constructs the realistic
// signal for its state and asserts the badge surfaces that state's label.
func TestRenderBadge_PerStateBadge(t *testing.T) {
	sym := tui.SymbolsFor(termenv.TrueColor)
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
			want: "cache",
		},
		{
			name: "skip",
			res:  executor.TaskResult{Status: executor.StatusSkipped},
			want: "skip",
		},
		{
			name:   "seed",
			res:    executor.TaskResult{Status: executor.StatusOK},
			seeded: true,
			want:   "seed",
		},
		{
			name:     "retry",
			res:      executor.TaskResult{},
			retrying: true,
			want:     "retry",
		},
		{
			name: "fail",
			res:  executor.TaskResult{Status: executor.StatusOK},
			err:  errors.New("boom"),
			// The failure badge shows the trimmed message inline.
			want: "fail: boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tui.RenderBadge(tc.res, tc.err, tc.seeded, tc.retrying, sym)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("RenderBadge %s = %q, want it to contain %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestBudgetGauge_ReportsSpendPercentage pins the budget gauge at three points:
// no spend (0%), partial spend (50%), and over the ceiling (200%), so the
// status bar surfaces how much of the workflow budget is consumed.
func TestBudgetGauge_ReportsSpendPercentage(t *testing.T) {
	sym := tui.SymbolsFor(termenv.TrueColor)
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
			got := tui.BudgetGauge(tc.spent, tc.max, sym)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("BudgetGauge(%v, %v) = %q, want empty", tc.spent, tc.max, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("BudgetGauge(%v, %v) = %q, want it to contain %q", tc.spent, tc.max, got, tc.want)
			}
		})
	}
}

// assertASCIINonEmpty fails when s is empty or carries any non-ASCII rune, the
// property the ASCII fallback profile must hold so a Unicode-less terminal still
// gets legible output.
func assertASCIINonEmpty(t *testing.T, label, s string) {
	t.Helper()
	if s == "" {
		t.Fatalf("%s: ASCII fallback rendered nothing; expected legible output", label)
	}
	for i, r := range s {
		if r >= utf8.RuneSelf {
			t.Fatalf("%s: non-ASCII rune %q at byte %d in %q", label, r, i, s)
		}
	}
}

// TestSymbols_ASCIIFallbackEmitsOnlyASCII pins that the ASCII fallback profile
// renders every badge and the gauge with ASCII-only runes. Each badge and the
// gauge is asserted in its own subtest so a regression names the exact renderer
// that emitted a non-ASCII rune rather than a single concatenated blob.
func TestSymbols_ASCIIFallbackEmitsOnlyASCII(t *testing.T) {
	sym := tui.SymbolsFor(termenv.Ascii)

	badges := []struct {
		name     string
		res      executor.TaskResult
		err      error
		seeded   bool
		retrying bool
	}{
		{name: "cache", res: executor.TaskResult{Status: executor.StatusOK, CacheHit: true}},
		{name: "skip", res: executor.TaskResult{Status: executor.StatusSkipped}},
		{name: "seed", res: executor.TaskResult{Status: executor.StatusOK}, seeded: true},
		{name: "retry", retrying: true},
		{name: "fail", res: executor.TaskResult{Status: executor.StatusOK}, err: errors.New("boom")},
	}
	for _, b := range badges {
		t.Run("badge/"+b.name, func(t *testing.T) {
			t.Parallel()
			assertASCIINonEmpty(t, "badge "+b.name, tui.RenderBadge(b.res, b.err, b.seeded, b.retrying, sym))
		})
	}

	t.Run("gauge", func(t *testing.T) {
		t.Parallel()
		assertASCIINonEmpty(t, "gauge", tui.BudgetGauge(0.5, 1, sym))
	})
}
