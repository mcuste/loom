package main

import (
	"strings"
	"testing"
	"time"
)

// TestParseAtTimeRejectsMalformedInput pins the CLI wrapper's flag-name framing:
// a bad --time or --date surfaces an error that names the offending flag so the
// user can fix it at the prompt. The domain behavior (rollover, explicit date)
// is covered by TestParseAtTime* in pkg/schedule.
func TestParseAtTimeRejectsMalformedInput(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 28, 16, 0, 0, 0, loc)

	cases := []struct {
		name      string
		timeStr   string
		dateStr   string
		wantInErr string
	}{
		{"non-numeric time", "noon", "", "--time"},
		{"out-of-range time", "25:00", "", "--time"},
		{"date-shaped time", "2026-07-01", "", "--time"},
		{"malformed date", "09:30", "07/01/2026", "--date"},
		{"out-of-range date", "09:30", "2026-13-40", "--date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAtTime(tc.timeStr, tc.dateStr, loc, now)
			if err == nil {
				t.Fatalf("parseAtTime(%q, %q) = nil error, want rejection", tc.timeStr, tc.dateStr)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q does not name %q", err.Error(), tc.wantInErr)
			}
		})
	}
}
