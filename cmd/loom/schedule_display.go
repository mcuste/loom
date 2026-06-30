package main

import (
	"time"
)

// formatFireTime renders an instant for display, or "-" when unset.
func formatFireTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04 MST")
}

// pick returns yes when cond holds, no otherwise: a tiny ternary so a bool-to-
// label mapping reads on one line instead of a four-line if/else.
func pick(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}
