package main

import (
	"fmt"
	"time"

	"github.com/mcuste/loom/pkg/schedule"
)

// triggerSummary renders a trigger for the ls table and confirmation lines.
func triggerSummary(t schedule.Trigger) string {
	if t.IsCron() {
		if t.TZ != "" {
			return fmt.Sprintf("cron %q %s", t.Cron, t.TZ)
		}
		return fmt.Sprintf("cron %q", t.Cron)
	}
	return "at " + formatFireTime(t.At)
}

// formatFireTime renders an instant for display, or "-" when unset.
func formatFireTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04 MST")
}
