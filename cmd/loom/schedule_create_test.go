package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseAtTimeRollsOverToNextDay(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 28, 16, 0, 0, 0, loc) // 16:00, after 15:00

	at, err := parseAtTime("15:00", "", loc, now)
	if err != nil {
		t.Fatalf("parseAtTime: %v", err)
	}
	want := time.Date(2026, 6, 29, 15, 0, 0, 0, loc)
	if !at.Equal(want) {
		t.Fatalf("at = %v, want %v (next day)", at, want)
	}
}

func TestParseAtTimeHonorsExplicitDate(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 28, 16, 0, 0, 0, loc)

	at, err := parseAtTime("09:30", "2026-07-01", loc, now)
	if err != nil {
		t.Fatalf("parseAtTime: %v", err)
	}
	want := time.Date(2026, 7, 1, 9, 30, 0, 0, loc)
	if !at.Equal(want) {
		t.Fatalf("at = %v, want %v", at, want)
	}
}

// TestParseAtTimeRejectsMalformedInput pins the two parse-rejection paths: a
// time that is not HH:MM and a date that is not YYYY-MM-DD each error, naming
// the offending flag so the user can fix it at the prompt.
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
