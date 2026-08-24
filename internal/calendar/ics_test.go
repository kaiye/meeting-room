package calendar

import (
	"strings"
	"testing"
	"time"
)

// day builds a local midnight, the form expandToday/occurrenceOn work in.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func vev(rrule, dtstart string) vevent {
	ev := vevent{
		props:  map[string]string{"DTSTART": dtstart, "SUMMARY": "x", "UID": "u"},
		params: map[string]string{},
		multi:  map[string][]string{},
	}
	if rrule != "" {
		ev.props["RRULE"] = rrule
	}
	return ev
}

// A COUNT-capped series must stop after its last occurrence. Real case: a
// bi-weekly that ran twice in June 2023 was still surfacing in 2026 because
// only UNTIL was honoured.
func TestCountEndsSeries(t *testing.T) {
	const rrule = "FREQ=WEEKLY;WKST=SU;COUNT=2;INTERVAL=2;BYDAY=TU"
	ev := vev(rrule, "20230606T110000")
	start, _, err := parseICSTime("20230606T110000", "", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		on   time.Time
		want bool
	}{
		{day(2023, 6, 6), true},   // 1st
		{day(2023, 6, 13), false}, // off-week
		{day(2023, 6, 20), true},  // 2nd, the last one
		{day(2023, 7, 4), false},  // 3rd would exceed COUNT
		{day(2026, 8, 11), false}, // the reported bug
	} {
		if _, ok := occurrenceOn(ev, start, tc.on, time.Local); ok != tc.want {
			t.Errorf("%s: got %v, want %v", tc.on.Format("2006-01-02"), ok, tc.want)
		}
	}
}

// Multi-day weeks make the index depend on where in the week DTSTART falls.
func TestOccurrenceIndexWeekly(t *testing.T) {
	rules := map[string]string{"FREQ": "WEEKLY", "BYDAY": "MO,WE,FR"}
	start := time.Date(2026, 8, 5, 10, 0, 0, 0, time.Local) // a Wednesday
	for _, tc := range []struct {
		on   time.Time
		want int
	}{
		{day(2026, 8, 5), 1},  // WE — DTSTART itself
		{day(2026, 8, 7), 2},  // FR, same week
		{day(2026, 8, 10), 3}, // MO, next week — the missing MO before DTSTART must not count
		{day(2026, 8, 14), 5}, // FR
		{day(2026, 8, 17), 6}, // MO
	} {
		days := int(tc.on.Sub(day(2026, 8, 5)).Hours() / 24)
		if got := occurrenceIndex(rules, start, tc.on, days, 1); got != tc.want {
			t.Errorf("%s: index %d, want %d", tc.on.Format("2006-01-02"), got, tc.want)
		}
	}
}

// EXDATE'd dates still consume a COUNT slot (RFC 5545 subtracts exclusions
// after the rule has generated its set), so the series must not run longer.
func TestCountIgnoresExdate(t *testing.T) {
	ev := vev("FREQ=WEEKLY;COUNT=3;BYDAY=TU", "20260804T110000")
	ev.multi["EXDATE"] = []string{"\x0020260811T110000"}
	start, _, _ := parseICSTime("20260804T110000", "", time.Local)
	for _, tc := range []struct {
		on   time.Time
		want bool
	}{
		{day(2026, 8, 4), true},
		{day(2026, 8, 11), false}, // excluded, but still the 2nd
		{day(2026, 8, 18), true},  // 3rd
		{day(2026, 8, 25), false}, // 4th — not shifted by the exclusion
	} {
		if _, ok := occurrenceOn(ev, start, tc.on, time.Local); ok != tc.want {
			t.Errorf("%s: got %v, want %v", tc.on.Format("2006-01-02"), ok, tc.want)
		}
	}
}

func TestCountDailyAndMonthly(t *testing.T) {
	daily := vev("FREQ=DAILY;COUNT=3;INTERVAL=2", "20260801T090000")
	dStart, _, _ := parseICSTime("20260801T090000", "", time.Local)
	for _, tc := range []struct {
		on   time.Time
		want bool
	}{
		{day(2026, 8, 1), true}, {day(2026, 8, 3), true},
		{day(2026, 8, 5), true}, {day(2026, 8, 7), false},
	} {
		if _, ok := occurrenceOn(daily, dStart, tc.on, time.Local); ok != tc.want {
			t.Errorf("daily %s: got %v, want %v", tc.on.Format("2006-01-02"), ok, tc.want)
		}
	}
	monthly := vev("FREQ=MONTHLY;COUNT=2", "20260315T090000")
	mStart, _, _ := parseICSTime("20260315T090000", "", time.Local)
	for _, tc := range []struct {
		on   time.Time
		want bool
	}{
		{day(2026, 3, 15), true}, {day(2026, 4, 15), true}, {day(2026, 5, 15), false},
	} {
		if _, ok := occurrenceOn(monthly, mStart, tc.on, time.Local); ok != tc.want {
			t.Errorf("monthly %s: got %v, want %v", tc.on.Format("2006-01-02"), ok, tc.want)
		}
	}
}

// UNTIL and cancelled/overridden instances kept working alongside COUNT.
func TestUntilAndCancelled(t *testing.T) {
	ev := vev("FREQ=WEEKLY;UNTIL=20260812T155959Z;BYDAY=TU", "20260804T110000")
	start, _, _ := parseICSTime("20260804T110000", "", time.Local)
	if _, ok := occurrenceOn(ev, start, day(2026, 8, 11), time.Local); !ok {
		t.Error("occurrence within UNTIL was dropped")
	}
	if _, ok := occurrenceOn(ev, start, day(2026, 8, 18), time.Local); ok {
		t.Error("occurrence past UNTIL was kept")
	}
	cancelled := vev("", "20260811T110000")
	cancelled.props["STATUS"] = "CANCELLED"
	if evs := expandToday([]vevent{cancelled}, day(2026, 8, 11).Add(9*time.Hour)); len(evs) != 0 {
		t.Errorf("cancelled event surfaced: %+v", evs)
	}
}

func TestIsResource(t *testing.T) {
	if !IsResource("room-a@resource.calendar.google.com", "") {
		t.Error("meeting room not detected")
	}
	if IsResource("alice@example.com", "INDIVIDUAL") {
		t.Error("person flagged as resource")
	}
	if !strings.Contains(weekdayCodes[time.Tuesday], "TU") {
		t.Error("weekday codes broken")
	}
}
