package tui

import (
	"fmt"
	"strings"

	"github.com/muesli/termenv"

	"github.com/mcuste/loom/pkg/executor"
)

// symbolSet is the glyph set the live renderer uses for state badges and the
// budget gauge. A Unicode set is used on capable terminals; an ASCII-only
// fallback is selected under the termenv.Ascii profile so output stays legible
// without Unicode.
type symbolSet struct {
	cacheHit string
	skipped  string
	seeded   string
	retry    string
	failed   string
	done     string
	barFull  string
	barEmpty string
	over     string
}

// symbolsFor returns the badge/gauge glyph set for color profile p, degrading
// to an ASCII-only set under termenv.Ascii.
func symbolsFor(p termenv.Profile) symbolSet {
	if p == termenv.Ascii {
		return symbolSet{
			cacheHit: "=",
			skipped:  "-",
			seeded:   "~",
			retry:    "@",
			failed:   "x",
			done:     "+",
			barFull:  "#",
			barEmpty: ".",
			over:     "!",
		}
	}
	return symbolSet{
		cacheHit: "⚡",
		skipped:  "⊘",
		seeded:   "↻",
		retry:    "↺",
		failed:   "✗",
		done:     "✓",
		barFull:  "█",
		barEmpty: "░",
		over:     "⚠",
	}
}

// badgeState carries the signals renderBadge needs to choose a task's live-view
// badge. The two booleans live in named fields rather than as adjacent
// positional parameters so they cannot be silently transposed at a call site.
type badgeState struct {
	res      executor.TaskResult
	err      error
	seeded   bool
	retrying bool
}

// renderBadge returns the live-view state badge for a task, using glyph set
// sym. It distinguishes a cache hit (res.CacheHit), a when-skip
// (res.Status == executor.StatusSkipped), a resume seed (seeded), an in-flight
// retry (retrying), and a failure (err != nil, whose trimmed message is shown
// inline) from a plain completion. The checks are ordered most- to
// least-specific: a failure or in-flight retry wins over the terminal-status
// badges.
func renderBadge(s badgeState, sym symbolSet) string {
	switch {
	case s.err != nil:
		return fmt.Sprintf("%s fail: %s", sym.failed, strings.TrimSpace(s.err.Error()))
	case s.retrying:
		return sym.retry + " retry"
	case s.res.Status == executor.StatusSkipped:
		return sym.skipped + " skip"
	case s.res.CacheHit:
		return sym.cacheHit + " cache"
	case s.seeded:
		return sym.seeded + " seed"
	default:
		return sym.done + " done"
	}
}

// gaugeWidth is the number of cells in the budget gauge's filled/empty bar.
const gaugeWidth = 10

// budgetGauge renders a compact spend gauge for a workflow budget: spent over
// max as a small filled/empty bar plus an integer percentage. The bar fill is
// clamped to the ceiling; when spend exceeds it, an over-limit flag is shown
// while the percentage still reports the true (>100%) value.
func budgetGauge(spent, max float64, sym symbolSet) string {
	if max <= 0 {
		return ""
	}
	frac := spent / max
	pct := frac * 100

	fill := int(frac*gaugeWidth + 0.5)
	over := fill > gaugeWidth
	if over {
		fill = gaugeWidth
	}
	bar := strings.Repeat(sym.barFull, fill) + strings.Repeat(sym.barEmpty, gaugeWidth-fill)

	out := fmt.Sprintf("[%s] %.0f%%", bar, pct)
	if over {
		out += " " + sym.over
	}
	return out
}

// loopRibbon renders the loop-iteration ribbon ("iteration n/max") from the
// run's loop metadata, or "" when the run has no loop.
func loopRibbon(meta RunMeta) string {
	if meta.Loop == nil {
		return ""
	}
	return fmt.Sprintf("iteration %d/%d", meta.Loop.N, meta.Loop.Max)
}
