package main

import (
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
