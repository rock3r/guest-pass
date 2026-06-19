package web

import (
	"net/http"
	"sort"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// calendarData backs the read-only calendar page (AC-2 / D-8): a month grid plus a
// chronological agenda of the host's scheduled streams. There is NO recurring/repeat
// control — recurring streams are v1.1 (D-8), so nothing here renders one.
type calendarData struct {
	MonthLabel string     // e.g. "July 2026"
	PrevMonth  string     // "YYYY-MM" for the ?month= nav
	NextMonth  string     // "YYYY-MM"
	Weeks      [][]calDay // month grid, weeks starting Monday
	Agenda     []agendaItem
}

// calDay is one cell of the month grid. Padding cells (before the 1st / after the last)
// are not InMonth and carry no day number.
type calDay struct {
	Day     int
	InMonth bool
	IsToday bool
	Streams []agendaItem
}

// agendaItem is a scheduled stream as the calendar renders it (display-ready). unix is the
// sort key (absolute UTC seconds) and is never read by templates.
type agendaItem struct {
	ID     string
	Title  string
	Status string
	When   string
	unix   int64
}

// calendar renders the host's read-only month + agenda calendar of scheduled streams
// (AC-2). The viewed month comes from ?month=YYYY-MM (default: the current month).
func (s *appServer) calendar(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	streams, err := s.store.ListStreamsByHost(r.Context(), host.ID)
	if err != nil {
		http.Error(w, "could not load streams", http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	s.rd.render(w, r, "calendar.html", pageData{
		Title: "Calendar", Nav: "calendar", Host: &navHost{Name: host.Name, IsAdmin: host.IsAdmin},
		Data: buildCalendar(now, parseMonth(r.URL.Query().Get("month"), now), streams),
	})
}

// parseMonth parses a "YYYY-MM" query value into the first instant of that month (UTC);
// any parse failure falls back to the month containing now, so a malformed ?month= never
// errors the page.
func parseMonth(v string, now time.Time) time.Time {
	if t, err := time.Parse("2006-01", v); err == nil {
		return t
	}
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// buildCalendar is the pure calendar projection (no I/O, no clock — now is passed in) so it
// is exhaustively table-tested. view is normalized to the first of the viewed month. The
// agenda is every scheduled stream (any month) in chronological order; the grid places only
// the viewed month's streams on their day. Unscheduled streams appear in neither.
func buildCalendar(now, view time.Time, streams []*store.Stream) calendarData {
	view = time.Date(view.Year(), view.Month(), 1, 0, 0, 0, 0, time.UTC)

	var agenda []agendaItem
	byDay := map[int][]agendaItem{}
	for _, s := range streams {
		if s.ScheduledAt == nil {
			continue
		}
		when := time.Unix(*s.ScheduledAt, 0).UTC()
		it := agendaItem{ID: s.ID, Title: s.Title, Status: s.Status, When: formatSchedule(s.ScheduledAt), unix: *s.ScheduledAt}
		agenda = append(agenda, it)
		if when.Year() == view.Year() && when.Month() == view.Month() {
			byDay[when.Day()] = append(byDay[when.Day()], it)
		}
	}
	sort.SliceStable(agenda, func(i, j int) bool { return agenda[i].unix < agenda[j].unix })
	for d, items := range byDay {
		sort.SliceStable(items, func(i, j int) bool { return items[i].unix < items[j].unix })
		byDay[d] = items
	}

	// Month grid, weeks starting Monday. leading = how many padding cells precede the 1st.
	daysInMonth := time.Date(view.Year(), view.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	leading := (int(view.Weekday()) - int(time.Monday) + 7) % 7
	isThisMonth := now.Year() == view.Year() && now.Month() == view.Month()

	var weeks [][]calDay
	var week []calDay
	push := func(c calDay) {
		week = append(week, c)
		if len(week) == 7 {
			weeks = append(weeks, week)
			week = nil
		}
	}
	for i := 0; i < leading; i++ {
		push(calDay{})
	}
	for d := 1; d <= daysInMonth; d++ {
		push(calDay{Day: d, InMonth: true, IsToday: isThisMonth && now.Day() == d, Streams: byDay[d]})
	}
	for len(week) > 0 {
		push(calDay{}) // pad the trailing week to a full 7
	}

	return calendarData{
		MonthLabel: view.Format("January 2006"),
		PrevMonth:  view.AddDate(0, -1, 0).Format("2006-01"),
		NextMonth:  view.AddDate(0, 1, 0).Format("2006-01"),
		Weeks:      weeks,
		Agenda:     agenda,
	}
}
