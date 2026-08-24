// Package calendar reads a subscribed ICS feed (Google Calendar's "secret
// address in iCal format", Outlook, etc.) and answers "what's on today".
// Recurrence support covers the common meeting patterns (DAILY / WEEKLY
// with BYDAY / simple MONTHLY / YEARLY, INTERVAL, UNTIL, COUNT, EXDATE, cancelled
// and overridden instances); exotic RRULEs may be missed.
package calendar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Attendee is an invitee: display name (CN param) and/or email.
type Attendee struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// IsResource reports whether an invitee is a meeting room / equipment
// rather than a person (Google books rooms as resource.calendar.google.com
// attendees; the iCal CUTYPE param marks them too).
func IsResource(email, cutype string) bool {
	email = strings.ToLower(email)
	cutype = strings.ToUpper(cutype)
	return strings.HasSuffix(email, "@resource.calendar.google.com") ||
		strings.Contains(email, "@resource.") ||
		cutype == "RESOURCE" || cutype == "ROOM"
}

type Event struct {
	// UID identifies this occurrence (event UID + date) so a recording can
	// be linked back to the calendar entry it was started from.
	UID       string     `json:"uid"`
	Title     string     `json:"title"`
	StartMs   int64      `json:"startMs"`
	EndMs     int64      `json:"endMs"`
	AllDay    bool       `json:"allDay"`
	Location  string     `json:"location"`
	Attendees []Attendee `json:"attendees"`
	// Description is the raw event body (agenda links etc.).
	Description string `json:"description"`
}

var cache struct {
	sync.Mutex
	url    string
	at     time.Time
	events []Event
	day    string
}

// Today fetches (with a 5-minute cache) and expands today's events.
// force skips the cache for an explicit user-triggered refresh.
func Today(ctx context.Context, icsURL string, force bool) ([]Event, error) {
	now := time.Now()
	day := now.Format("2006-01-02")
	cache.Lock()
	if !force && cache.url == icsURL && cache.day == day && time.Since(cache.at) < 5*time.Minute {
		evs := cache.events
		cache.Unlock()
		return evs, nil
	}
	cache.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, icsURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("日历订阅地址不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("日历订阅返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}

	events := expandToday(parseICS(string(body)), now)
	cache.Lock()
	cache.url, cache.at, cache.events, cache.day = icsURL, now, events, day
	cache.Unlock()
	return events, nil
}

type vevent struct {
	props  map[string]string   // KEY -> value (last wins)
	params map[string]string   // KEY -> raw params ("TZID=...;VALUE=DATE")
	multi  map[string][]string // repeatable keys: ATTENDEE, EXDATE
}

func parseICS(raw string) []vevent {
	// Unfold continuation lines (RFC 5545: lines starting with space/tab).
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
		} else {
			lines = append(lines, line)
		}
	}
	var events []vevent
	var cur *vevent
	for _, line := range lines {
		switch {
		case line == "BEGIN:VEVENT":
			cur = &vevent{props: map[string]string{}, params: map[string]string{}, multi: map[string][]string{}}
		case line == "END:VEVENT":
			if cur != nil {
				events = append(events, *cur)
				cur = nil
			}
		case cur != nil:
			colon := strings.Index(line, ":")
			if colon < 0 {
				continue
			}
			keyPart, value := line[:colon], line[colon+1:]
			key := keyPart
			params := ""
			if i := strings.Index(keyPart, ";"); i >= 0 {
				key, params = keyPart[:i], keyPart[i+1:]
			}
			key = strings.ToUpper(key)
			if key == "ATTENDEE" || key == "EXDATE" {
				cur.multi[key] = append(cur.multi[key], params+"\x00"+value)
			} else {
				cur.props[key] = value
				cur.params[key] = params
			}
		}
	}
	return events
}

func parseICSTime(value, params string, loc *time.Location) (time.Time, bool, error) {
	allDay := strings.Contains(params, "VALUE=DATE") || len(value) == 8
	if tz := paramValue(params, "TZID"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	switch {
	case allDay:
		t, err := time.ParseInLocation("20060102", value, loc)
		return t, true, err
	case strings.HasSuffix(value, "Z"):
		t, err := time.Parse("20060102T150405Z", value)
		return t, false, err
	default:
		t, err := time.ParseInLocation("20060102T150405", value, loc)
		return t, false, err
	}
}

func paramValue(params, key string) string {
	for _, p := range strings.Split(params, ";") {
		if strings.HasPrefix(p, key+"=") {
			return strings.TrimPrefix(p, key+"=")
		}
	}
	return ""
}

func unescapeText(s string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return r.Replace(s)
}

// expandToday returns concrete occurrences intersecting today, local time.
func expandToday(events []vevent, now time.Time) []Event {
	loc := now.Location()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)

	// Overridden instances (RECURRENCE-ID) suppress the base occurrence.
	overridden := map[string]bool{} // UID + local date of the recurrence id
	for _, ev := range events {
		if rid, ok := ev.props["RECURRENCE-ID"]; ok {
			t, _, err := parseICSTime(rid, ev.params["RECURRENCE-ID"], loc)
			if err == nil {
				overridden[ev.props["UID"]+"@"+t.In(loc).Format("20060102")] = true
			}
		}
	}

	var out []Event
	for _, ev := range events {
		if ev.props["STATUS"] == "CANCELLED" {
			continue
		}
		start, allDay, err := parseICSTime(ev.props["DTSTART"], ev.params["DTSTART"], loc)
		if err != nil {
			continue
		}
		var dur time.Duration
		if e, _, err := parseICSTime(ev.props["DTEND"], ev.params["DTEND"], loc); err == nil {
			dur = e.Sub(start)
		} else if allDay {
			dur = 24 * time.Hour
		} else {
			dur = time.Hour
		}

		occ, ok := occurrenceOn(ev, start, dayStart, loc)
		if !ok {
			continue
		}
		// Base occurrence replaced by an override? (The override VEVENT is
		// its own entry and lands here via the non-recurring path.)
		if _, isOverride := ev.props["RECURRENCE-ID"]; !isOverride && ev.props["RRULE"] != "" &&
			overridden[ev.props["UID"]+"@"+occ.In(loc).Format("20060102")] {
			continue
		}
		end := occ.Add(dur)
		if !occ.Before(dayEnd) || !end.After(dayStart) {
			continue
		}
		attendees := []Attendee{}
		for _, a := range ev.multi["ATTENDEE"] {
			params, value, _ := strings.Cut(a, "\x00")
			email := strings.TrimPrefix(strings.ToLower(value), "mailto:")
			// Rooms and equipment are booked as attendees; they are not people.
			if IsResource(email, paramValue(params, "CUTYPE")) {
				continue
			}
			name := strings.Trim(paramValue(params, "CN"), `"`)
			if name == "" {
				name = email
			}
			attendees = append(attendees, Attendee{Name: name, Email: email})
		}
		out = append(out, Event{
			UID:         ev.props["UID"] + "@" + occ.In(loc).Format("20060102"),
			Title:       unescapeText(ev.props["SUMMARY"]),
			StartMs:     occ.UnixMilli(),
			EndMs:       end.UnixMilli(),
			AllDay:      allDay,
			Location:    unescapeText(ev.props["LOCATION"]),
			Attendees:   attendees,
			Description: unescapeText(ev.props["DESCRIPTION"]),
		})
	}
	// Sort by start.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartMs < out[j-1].StartMs; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

var weekdayCodes = map[time.Weekday]string{
	time.Sunday: "SU", time.Monday: "MO", time.Tuesday: "TU", time.Wednesday: "WE",
	time.Thursday: "TH", time.Friday: "FR", time.Saturday: "SA",
}

// weeklyByday is the rule's BYDAY, defaulting to DTSTART's own weekday.
func weeklyByday(rules map[string]string, sLoc time.Time) string {
	if byday := rules["BYDAY"]; byday != "" {
		return byday
	}
	return weekdayCodes[sLoc.Weekday()]
}

// occurrenceIndex is the 1-based position of dayStart within the series,
// for enforcing COUNT. Dates removed by EXDATE still occupy a slot: RFC 5545
// applies exclusions *after* the rule has generated its set.
func occurrenceIndex(rules map[string]string, sLoc, dayStart time.Time, days, interval int) int {
	switch rules["FREQ"] {
	case "DAILY":
		return days/interval + 1
	case "WEEKLY":
		byday := weeklyByday(rules, sLoc)
		var week []int
		for d, code := range weekdayCodes {
			if strings.Contains(byday, code) {
				week = append(week, int(d))
			}
		}
		from, to := int(sLoc.Weekday()), int(dayStart.Weekday())
		// Weeks elapsed, counting only the active ones (every INTERVAL-th).
		weeks := (days + from - to) / 7 / interval
		if weeks == 0 {
			n := 1 // same week as DTSTART: just the earlier BYDAYs in it
			for _, d := range week {
				if d >= from && d < to {
					n++
				}
			}
			return n
		}
		first, before := 0, 0
		for _, d := range week {
			if d >= from {
				first++ // DTSTART's own week is entered partway through
			}
			if d < to {
				before++ // days already past in the current week
			}
		}
		return first + (weeks-1)*len(week) + before + 1
	case "MONTHLY":
		months := (dayStart.Year()-sLoc.Year())*12 + int(dayStart.Month()) - int(sLoc.Month())
		return months/interval + 1
	case "YEARLY":
		return dayStart.Year() - sLoc.Year() + 1
	}
	return 1
}

// occurrenceOn reports whether the event has an occurrence starting on the
// given day (or, for non-recurring events, uses its own start).
func occurrenceOn(ev vevent, start time.Time, dayStart time.Time, loc *time.Location) (time.Time, bool) {
	rrule := ev.props["RRULE"]
	if rrule == "" {
		return start.In(loc), true
	}
	if start.In(loc).After(dayStart.AddDate(0, 0, 1)) {
		return time.Time{}, false
	}
	rules := map[string]string{}
	for _, kv := range strings.Split(rrule, ";") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			rules[strings.ToUpper(k)] = v
		}
	}
	interval := 1
	if v, err := strconv.Atoi(rules["INTERVAL"]); err == nil && v > 0 {
		interval = v
	}
	// The candidate occurrence: today's date at the event's start clock
	// time (in the event's own timezone, converted to local).
	sLoc := start.In(loc)
	occ := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(),
		sLoc.Hour(), sLoc.Minute(), sLoc.Second(), 0, loc)
	if occ.Before(sLoc) {
		return time.Time{}, false
	}
	if until := rules["UNTIL"]; until != "" {
		if u, _, err := parseICSTime(until, "", loc); err == nil && occ.After(u) {
			return time.Time{}, false
		}
	}
	startDay := time.Date(sLoc.Year(), sLoc.Month(), sLoc.Day(), 0, 0, 0, 0, loc)
	days := int(dayStart.Sub(startDay).Hours() / 24)
	match := false
	switch rules["FREQ"] {
	case "DAILY":
		match = days%interval == 0
	case "WEEKLY":
		byday := weeklyByday(rules, sLoc)
		weekOK := ((days+int(sLoc.Weekday())-int(dayStart.Weekday()))/7)%interval == 0
		match = weekOK && strings.Contains(byday, weekdayCodes[dayStart.Weekday()])
	case "MONTHLY":
		months := (dayStart.Year()-sLoc.Year())*12 + int(dayStart.Month()) - int(sLoc.Month())
		match = months%interval == 0 && dayStart.Day() == sLoc.Day()
	case "YEARLY":
		match = dayStart.Month() == sLoc.Month() && dayStart.Day() == sLoc.Day()
	}
	if !match {
		return time.Time{}, false
	}
	// COUNT ends the series after N occurrences instead of on a date. Without
	// it a finished series (Google writes COUNT when a recurrence is capped or
	// its tail is deleted) would keep producing occurrences forever.
	if count, err := strconv.Atoi(rules["COUNT"]); err == nil && count > 0 &&
		occurrenceIndex(rules, sLoc, dayStart, days, interval) > count {
		return time.Time{}, false
	}
	// EXDATE: this specific occurrence was removed.
	for _, ex := range ev.multi["EXDATE"] {
		params, value, _ := strings.Cut(ex, "\x00")
		for _, v := range strings.Split(value, ",") {
			if t, _, err := parseICSTime(v, params, loc); err == nil &&
				t.In(loc).Format("20060102") == dayStart.Format("20060102") {
				return time.Time{}, false
			}
		}
	}
	return occ, true
}
