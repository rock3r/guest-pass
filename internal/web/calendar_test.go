package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rock3r/guest-pass/internal/store"
)

func schedAt(y int, m time.Month, d, hh, mm int) *int64 {
	v := time.Date(y, m, d, hh, mm, 0, 0, time.UTC).Unix()
	return &v
}

// findDay returns the in-month grid cell for the given day-of-month, or fails.
func findDay(t *testing.T, cal calendarData, day int) calDay {
	t.Helper()
	for _, week := range cal.Weeks {
		for _, cell := range week {
			if cell.InMonth && cell.Day == day {
				return cell
			}
		}
	}
	t.Fatalf("no in-month cell for day %d", day)
	return calDay{}
}

// AC-2 (pure): the month grid places a scheduled stream on its day; the agenda lists every
// scheduled stream (any month) in chronological order; unscheduled streams appear in
// neither. Month label + prev/next navigation are derived from the viewed month.
func TestBuildCalendar_PlacesScheduledStreamsAndExcludesUnscheduled(t *testing.T) {
	view := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)

	streams := []*store.Stream{
		{ID: "s1", Title: "In Month", Status: store.StreamScheduled, ScheduledAt: schedAt(2026, time.July, 20, 15, 30)},
		{ID: "s2", Title: "Next Month", Status: store.StreamScheduled, ScheduledAt: schedAt(2026, time.August, 3, 9, 0)},
		{ID: "s3", Title: "Unscheduled", Status: store.StreamDraft, ScheduledAt: nil},
	}
	cal := buildCalendar(now, view, streams)

	if cal.MonthLabel != "July 2026" {
		t.Fatalf("MonthLabel = %q, want July 2026", cal.MonthLabel)
	}
	if cal.PrevMonth != "2026-06" || cal.NextMonth != "2026-08" {
		t.Fatalf("prev/next = %q/%q, want 2026-06/2026-08", cal.PrevMonth, cal.NextMonth)
	}

	// Agenda: both scheduled streams (in chronological order), never the unscheduled one.
	if len(cal.Agenda) != 2 {
		t.Fatalf("agenda len = %d, want 2 (scheduled only)", len(cal.Agenda))
	}
	if cal.Agenda[0].ID != "s1" || cal.Agenda[1].ID != "s2" {
		t.Fatalf("agenda order = %s,%s, want s1,s2 (ascending by time)", cal.Agenda[0].ID, cal.Agenda[1].ID)
	}
	for _, it := range cal.Agenda {
		if it.ID == "s3" {
			t.Fatal("agenda must not include the unscheduled stream")
		}
	}

	// Grid: July 20 holds s1; no in-month cell holds the next-month or unscheduled stream.
	d20 := findDay(t, cal, 20)
	if len(d20.Streams) != 1 || d20.Streams[0].ID != "s1" {
		t.Fatalf("day 20 streams = %+v, want exactly s1", d20.Streams)
	}
	for _, week := range cal.Weeks {
		for _, cell := range week {
			for _, st := range cell.Streams {
				if st.ID == "s2" || st.ID == "s3" {
					t.Fatalf("cell day %d wrongly holds %s", cell.Day, st.ID)
				}
			}
		}
	}

	// "Today" is flagged on the matching in-month day.
	if d15 := findDay(t, cal, 15); !d15.IsToday {
		t.Fatal("day 15 should be flagged IsToday")
	}
	if findDay(t, cal, 20).IsToday {
		t.Fatal("day 20 should not be IsToday")
	}
}

func TestBuildCalendar_UsesHostTimezoneForDateBoundaries(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	view := time.Date(2026, time.July, 1, 0, 0, 0, 0, loc)
	// 23:30 on July 31 for the host is August 1 in UTC.
	scheduled := time.Date(2026, time.August, 1, 6, 30, 0, 0, time.UTC).Unix()
	cal := buildCalendar(view, view, []*store.Stream{{ID: "late-july", Title: "Late July", Status: store.StreamScheduled, ScheduledAt: &scheduled}})

	if got := findDay(t, cal, 31).Streams; len(got) != 1 || got[0].ID != "late-july" {
		t.Fatalf("July 31 streams = %+v, want late-july", got)
	}
	if len(cal.Agenda) != 1 || !strings.Contains(cal.Agenda[0].When, "Jul 31") {
		t.Fatalf("agenda = %+v, want July 31 in the host timezone", cal.Agenda)
	}
}

// The grid covers the whole month in full weeks (7-day rows), with leading/trailing
// padding cells that are not InMonth.
func TestBuildCalendar_GridShapeAndPadding(t *testing.T) {
	view := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC) // July 1 2026 is a Wednesday
	cal := buildCalendar(view, view, nil)

	for i, week := range cal.Weeks {
		if len(week) != 7 {
			t.Fatalf("week %d has %d cells, want 7", i, len(week))
		}
	}
	// 31 in-month days exactly, each appearing once.
	seen := map[int]int{}
	for _, week := range cal.Weeks {
		for _, cell := range week {
			if cell.InMonth {
				seen[cell.Day]++
			}
		}
	}
	if len(seen) != 31 {
		t.Fatalf("in-month days = %d, want 31", len(seen))
	}
	for d, n := range seen {
		if n != 1 {
			t.Fatalf("day %d appears %d times, want once", d, n)
		}
	}
}

// AC-2 / route: the calendar page lists a host's scheduled stream and links to its detail;
// a foreign host's stream never appears. Unauthenticated → gated by RequireHost.
func TestApp_CalendarRoute(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")

	id := a.createStream(t, alice, "Scheduled Show")
	// Give it a schedule via the edit form so it lands on the calendar.
	if rec := a.formReq(t, http.MethodPost, "/app/streams/"+id,
		url.Values{"title": {"Scheduled Show"}, "scheduled_at": {"2026-09-10T18:00"}}, alice); rec.Code != http.StatusSeeOther {
		t.Fatalf("schedule update = %d", rec.Code)
	}
	bobID := a.createStream(t, bob, "Bob Scheduled")
	_ = a.formReq(t, http.MethodPost, "/app/streams/"+bobID,
		url.Values{"title": {"Bob Scheduled"}, "scheduled_at": {"2026-09-11T18:00"}}, bob)

	rec := a.req(t, http.MethodGet, "/app/calendar?month=2026-09", "", alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app/calendar = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Scheduled Show") {
		t.Fatalf("calendar missing own scheduled stream; body:\n%s", body)
	}
	if strings.Contains(body, "Bob Scheduled") {
		t.Fatal("calendar leaked another host's stream")
	}
	if !strings.Contains(body, "/app/streams/"+id) {
		t.Fatal("calendar does not link to the stream's detail/edit page")
	}

	if rec := a.req(t, http.MethodGet, "/app/calendar", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated calendar = %d, want 401", rec.Code)
	}
}
