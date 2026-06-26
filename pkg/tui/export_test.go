package tui

import (
	"github.com/muesli/termenv"

	"github.com/mcuste/loom/pkg/executor"
)

// This file exposes the live-view badge and gauge renderers to the external
// tui_test package. It is a test-only build (the _test.go suffix), so these
// accessors never widen the permanent public API; they give the black-box test
// a stable seam instead of reaching into unexported symbols.

// SymbolSet is the test alias for the unexported glyph set.
type SymbolSet = symbolSet

// SymbolsFor returns the glyph set for color profile p.
func SymbolsFor(p termenv.Profile) SymbolSet { return symbolsFor(p) }

// RenderBadge renders the live-view state badge for the given task signals.
func RenderBadge(res executor.TaskResult, err error, seeded, retrying bool, sym SymbolSet) string {
	return renderBadge(badgeState{res: res, err: err, seeded: seeded, retrying: retrying}, sym)
}

// BudgetGauge renders the compact spend gauge for a workflow budget.
func BudgetGauge(spent, max float64, sym SymbolSet) string {
	return budgetGauge(spent, max, sym)
}
