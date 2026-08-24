package jobs

import (
	"testing"
	"time"
)

func TestWithDate(t *testing.T) {
	at := time.Date(2026, 8, 12, 11, 0, 0, 0, time.Local)
	for _, tc := range []struct{ in, want string }{
		{"Studio 周会", "Studio 周会 2026-08-12"},
		// Already dated by the model (extracted from a calendar invite) —
		// don't stutter.
		{"Studio 周会 2026-08-12", "Studio 周会 2026-08-12"},
		{"2026-08-12 排期评审", "2026-08-12 排期评审"},
	} {
		if got := withDate(tc.in, at); got != tc.want {
			t.Errorf("withDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The date must be the meeting's, not today's.
	old := time.Date(2026, 7, 1, 9, 0, 0, 0, time.Local)
	if got := withDate("回顾会", old); got != "回顾会 2026-07-01" {
		t.Errorf("got %q, want 回顾会 2026-07-01", got)
	}
}

func TestCleanTitle(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  《技术方案评审》 ", "技术方案评审"},
		{"\"周会\"\n(理由:…)", "周会"},
		{"排期会。", "排期会"},
	} {
		if got := cleanTitle(tc.in); got != tc.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
