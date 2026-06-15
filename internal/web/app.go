package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// appServer renders the host's day-to-day app shell — the dashboard and stream CRUD now,
// the calendar / stream-detail / settings surfaces in later M4 PRs (D-32). It is
// server-rendered HTML with NO JavaScript (CONVENTIONS §3.1): forms POST to dedicated
// handlers that mutate then redirect (POST-redirect-GET). State-changing POSTs are
// CSRF-safe via the SameSite=Lax session cookie — a cross-site form submission never
// carries it (auth/session.go), so no separate CSRF token is needed. Every route is
// mounted behind RequireHost, which gates pending/suspended hosts (EN-6).
type appServer struct {
	store *store.Store
	rd    *renderer
}

// dashStream is one stream row as the dashboard renders it (display-ready).
type dashStream struct {
	ID          string
	Title       string
	Status      string
	ScheduledAt string // formatted absolute UTC; empty when unscheduled
	HasSchedule bool
}

type dashboardData struct {
	Streams []dashStream
}

// streamFormData backs the edit form; Title/ScheduledAt/DurationMin are pre-formatted for
// the corresponding HTML inputs (datetime-local / number).
type streamFormData struct {
	ID          string
	Title       string
	ScheduledAt string
	DurationMin string
	Error       string
}

// dashboard lists the signed-in host's streams (title, status, scheduled time) with the
// create form (AC-1). A foreign host's streams never appear — the query is scoped by host.
func (s *appServer) dashboard(w http.ResponseWriter, r *http.Request) {
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
	rows := make([]dashStream, 0, len(streams))
	for _, st := range streams {
		rows = append(rows, dashStream{
			ID:          st.ID,
			Title:       st.Title,
			Status:      st.Status,
			ScheduledAt: formatSchedule(st.ScheduledAt),
			HasSchedule: st.ScheduledAt != nil,
		})
	}
	s.rd.render(w, r, "dashboard.html", pageData{
		Title: "Your streams", Nav: "dashboard", Host: &navHost{Name: host.Name},
		Data: dashboardData{Streams: rows},
	})
}

// createStream creates a stream from the server-rendered form and redirects to the
// dashboard (POST-redirect-GET). A blank title is rejected with 400 (AC-1).
func (s *appServer) createStream(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	title, sched, dur, err := parseStreamForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.store.CreateStream(r.Context(), store.CreateStreamParams{
		HostID: host.ID, Title: title, ScheduledAt: sched, DurationMin: dur,
	}); err != nil {
		http.Error(w, "could not create stream", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// editStreamForm renders the edit form prefilled from the current stream (AC-1).
func (s *appServer) editStreamForm(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	s.rd.render(w, r, "streamedit.html", pageData{
		Title: "Edit stream", Nav: "dashboard", Host: &navHost{Name: host.Name},
		Data: streamFormData{
			ID:          st.ID,
			Title:       st.Title,
			ScheduledAt: formatDateTimeLocal(st.ScheduledAt),
			DurationMin: formatDurationField(st.DurationMin),
		},
	})
}

// updateStream applies the edit form to an owned stream, then redirects to the dashboard.
func (s *appServer) updateStream(w http.ResponseWriter, r *http.Request) {
	_, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	title, sched, dur, err := parseStreamForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st.Title = title
	st.ScheduledAt = sched
	st.DurationMin = dur
	if err := s.store.UpdateStream(r.Context(), st); err != nil {
		http.Error(w, "could not update stream", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// deleteStream removes an owned stream (cascading to its passes/sessions, FK) and
// redirects to the dashboard.
func (s *appServer) deleteStream(w http.ResponseWriter, r *http.Request) {
	_, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteStream(r.Context(), st.ID); err != nil {
		http.Error(w, "could not delete stream", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// ownedStream loads the {id} stream and confirms it belongs to the signed-in host. A
// missing OR foreign id both answer a plain 404 so a host can't probe others' ids
// (mirrors the JSON API's ownedStream — apiServer.ownedStream — but renders plain text).
func (s *appServer) ownedStream(w http.ResponseWriter, r *http.Request) (*store.Host, *store.Stream, bool) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, nil, false
	}
	st, err := s.store.GetStream(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && st.HostID != host.ID) {
		http.Error(w, "stream not found", http.StatusNotFound)
		return nil, nil, false
	}
	if err != nil {
		http.Error(w, "could not load stream", http.StatusInternalServerError)
		return nil, nil, false
	}
	return host, st, true
}

// parseStreamForm reads and validates the shared create/edit stream form. Title is
// required (after trimming) and length-capped; schedule and duration are optional. An
// empty field clears its column (the form is the full editable state).
func parseStreamForm(r *http.Request) (title string, scheduledAt, durationMin *int64, err error) {
	if perr := r.ParseForm(); perr != nil {
		return "", nil, nil, errors.New("invalid form")
	}
	title = strings.TrimSpace(r.PostFormValue("title"))
	if title == "" {
		return "", nil, nil, errors.New("title is required")
	}
	if len(title) > maxStreamTitle {
		return "", nil, nil, errors.New("title is too long")
	}
	if v := strings.TrimSpace(r.PostFormValue("scheduled_at")); v != "" {
		ts, perr := parseDateTimeLocal(v)
		if perr != nil {
			return "", nil, nil, errors.New("invalid scheduled time")
		}
		scheduledAt = &ts
	}
	if v := strings.TrimSpace(r.PostFormValue("duration_min")); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 0 || n > maxDurationMin {
			return "", nil, nil, errors.New("invalid duration")
		}
		durationMin = &n
	}
	return title, scheduledAt, durationMin, nil
}

const (
	maxStreamTitle = 200  // matches the form input maxlength; keeps a hostile/huge title out of the DB
	maxDurationMin = 1440 // a single day; the recurring/multi-day case is out of scope (D-8)
)

// datetimeLocalLayouts are the formats an <input type="datetime-local"> submits — without
// seconds, and (some browsers) with. The value carries no timezone, so we interpret it as
// UTC and store absolute UTC seconds (EN-25); the edit form formats back in UTC too, so
// the value round-trips. Host-local rendering is deferred polish (DEF-2).
var datetimeLocalLayouts = []string{"2006-01-02T15:04", "2006-01-02T15:04:05"}

func parseDateTimeLocal(v string) (int64, error) {
	for _, layout := range datetimeLocalLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, errors.New("unrecognized datetime")
}

// formatSchedule renders a stored schedule for display on the dashboard (absolute UTC).
func formatSchedule(ts *int64) string {
	if ts == nil {
		return ""
	}
	return time.Unix(*ts, 0).UTC().Format("Mon Jan 2, 2006 15:04 UTC")
}

// formatDateTimeLocal renders a stored schedule for a datetime-local input value (UTC).
func formatDateTimeLocal(ts *int64) string {
	if ts == nil {
		return ""
	}
	return time.Unix(*ts, 0).UTC().Format("2006-01-02T15:04")
}

// formatDurationField renders a stored duration for a number input value.
func formatDurationField(d *int64) string {
	if d == nil {
		return ""
	}
	return strconv.FormatInt(*d, 10)
}
