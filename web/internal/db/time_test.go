package db

import "testing"

func TestBeijingTimestamp(t *testing.T) {
	for input, want := range map[string]string{
		"2026-07-20 04:30:00":       "2026-07-20T12:30:00+08:00",
		"2026-07-20T04:30:00Z":      "2026-07-20T12:30:00+08:00",
		"2026-07-20T12:30:00+08:00": "2026-07-20T12:30:00+08:00",
	} {
		if got := BeijingTimestamp(input); got != want {
			t.Fatalf("BeijingTimestamp(%q)=%q, want %q", input, got, want)
		}
	}
}
