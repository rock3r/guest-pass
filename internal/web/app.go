package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// appServer renders the host's day-to-day app shell — the dashboard and stream CRUD now,
// the calendar / stream-detail / settings surfaces in later M4 PRs (D-32). It is
// server-rendered HTML with NO JavaScript (CONVENTIONS §3.1): forms POST to dedicated
// handlers that mutate then redirect (POST-redirect-GET). State-changing POSTs are
// CSRF-safe via the SameSite=Lax session cookie — a cross-site form submission never
// carries it (auth/session.go), so no separate CSRF token is needed. Every route is
// mounted behind RequireHost, which gates pending/suspended hosts (EN-6).
type appServer struct {
	store     *store.Store
	rd        *renderer
	hasher    *token.Hasher       // magic-link + slot token hashing (EN-5)
	mailer    mail.Mailer         // invite delivery
	baseURL   string              // absolute origin for building magic links + OBS source URLs
	reveals   *revealStore        // one-time post-redirect reveal of a just-minted secret
	hub       *signaling.Hub      // to tear down a live OBS source on slot-token rotation (D-22); may be nil
	binds     *bindingLocks       // serialize Go-live's pre-live binding replay with /ws joins + picker PUTs (D-20)
	auth      *auth.Authenticator // clears the session cookie on account erasure (settings delete form, AC-5)
	liveCheck LiveChecker         // D-29 live-verify (validate + persist a linked channel); may be nil
	trust     auth.TrustPolicy    // D-36 progressive-trust invite/stream quotas; zero value = disabled
}

// dashStream is one stream row as the dashboard renders it (display-ready).
type dashStream struct {
	ID          string
	Title       string
	Status      string
	ScheduledAt string // formatted absolute UTC; empty when unscheduled
	HasSchedule bool
	MonthShort  string // e.g. "Jun"; empty when unscheduled
	DayNum      string // e.g. "15"; empty when unscheduled
	TimeShort   string // e.g. "18:00 UTC"; empty when unscheduled
}

// greenroom renders the host's live control room in the authenticated app shell. The room's
// realtime state still comes from the island over WebSocket; this server-rendered context gives a
// host an immediate, safe workspace frame before the connection is established.
func (s *appServer) greenroom(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	data := pageData{Title: "Greenroom", Nav: "greenroom", Host: hostNav(host)}
	sess, err := s.store.ActiveSession(r.Context(), host.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// A host may open the room before declaring a stream live. The island keeps its
		// existing pre-live behavior; omit stream-specific navigation until there is one.
	case err != nil:
		http.Error(w, "could not load active stream", http.StatusInternalServerError)
		return
	default:
		st, err := s.store.GetStream(r.Context(), sess.StreamID)
		if err != nil {
			http.Error(w, "could not load active stream", http.StatusInternalServerError)
			return
		}
		data.Title = st.Title + " · Greenroom"
		data.CurrentStream = &navStream{ID: st.ID, Title: st.Title, IsLive: true}
	}

	s.rd.render(w, r, "greenroom.html", data)
}

type dashboardData struct {
	Streams         []dashStream
	UpcomingStreams []dashStream      // draft or scheduled, for the "Up next" panel
	RecentStreams   []dashStream      // ended, for the "Recent" panel
	QuotaError      bool              // ?error=stream-quota — the host hit their progressive-trust stream cap (D-36)
	UpcomingCount   int               // streams that are draft or scheduled
	PassCount       int               // all-time passes issued by this host
	HoursStreamed   string            // formatted total scheduled hours, e.g. "46h" or "0h"
	LiveStream      *liveStreamBanner // non-nil when the host has an active live session
}

// liveStreamBanner carries the minimal identity of a currently live stream for the dashboard
// live-stream banner. It is populated only when the host has a live session.
type liveStreamBanner struct {
	ID    string
	Title string
}

// settingsData backs the host's account settings page (AC-3..5): the host's own Google identity
// (Email is read-only; Name is editable via the amend form) plus PRG flash flags from the GDPR
// forms. Email is the host's OWN account email on their own page (not a cross-host leak, EN-8).
type settingsData struct {
	Name           string
	Email          string
	Saved          bool // ?saved=1 after a successful amend
	NameError      bool // ?error=name — the amend name was empty
	LiveError      bool // ?error=live — delete refused while a live session exists (D-M5-3)
	LastAdminError bool // ?error=last-admin — delete refused: host is the only active admin (AC-9)
	ConfirmError   bool // ?error=confirm — delete POST arrived without the confirmation field
}

// settings renders the host's account settings page (AC-3..5): a READ-ONLY account card (the host's
// Google identity), the FUNCTIONAL GDPR self-service controls — export download link, amend-name
// form, delete-account form (D-37) — and a pointer to the per-stream quality ceiling (greenroom,
// D-19). Host-only (RequireHost); the email shown is the host's OWN account email, never logged
// (EN-16). Flash flags come from the amend/delete forms' PRG redirect query.
func (s *appServer) settings(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	s.rd.render(w, r, "settings.html", pageData{
		Title: "Settings", Nav: "settings", Host: hostNav(host),
		Data: settingsData{
			Name:           host.Name,
			Email:          host.Email,
			Saved:          q.Get("saved") == "1",
			NameError:      q.Get("error") == "name",
			LiveError:      q.Get("error") == "live",
			LastAdminError: q.Get("error") == "last-admin",
			ConfirmError:   q.Get("error") == "confirm",
		},
	})
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
	var upcoming, recent []dashStream
	var upcomingCount int
	var liveStream *liveStreamBanner
	for _, st := range streams {
		ds := dashStream{
			ID:          st.ID,
			Title:       st.Title,
			Status:      st.Status,
			ScheduledAt: formatSchedule(st.ScheduledAt),
			HasSchedule: st.ScheduledAt != nil,
		}
		if st.ScheduledAt != nil {
			t := time.Unix(*st.ScheduledAt, 0).UTC()
			ds.MonthShort = t.Format("Jan")
			ds.DayNum = t.Format("2")
			ds.TimeShort = t.Format("15:04 UTC")
		}
		rows = append(rows, ds)
		switch st.Status {
		case "draft", "scheduled":
			upcomingCount++
			if len(upcoming) < 3 {
				upcoming = append(upcoming, ds)
			}
		case "ended":
			if len(recent) < 3 {
				recent = append(recent, ds)
			}
		case "live":
			if liveStream == nil {
				liveStream = &liveStreamBanner{ID: st.ID, Title: st.Title}
			}
		}
	}

	passCount, err := s.store.CountAllPassesByHost(r.Context(), host.ID)
	if err != nil {
		http.Error(w, "could not load dashboard stats", http.StatusInternalServerError)
		return
	}
	totalMins, err := s.store.SumStreamMinutesByHost(r.Context(), host.ID)
	if err != nil {
		http.Error(w, "could not load dashboard stats", http.StatusInternalServerError)
		return
	}
	hoursStreamed := strconv.Itoa(totalMins/60) + "h"

	s.rd.render(w, r, "dashboard.html", pageData{
		Title: "Your streams", Nav: "dashboard", Host: hostNav(host),
		Data: dashboardData{
			Streams:         rows,
			UpcomingStreams: upcoming,
			RecentStreams:   recent,
			QuotaError:      r.URL.Query().Get("error") == "stream-quota",
			UpcomingCount:   upcomingCount,
			PassCount:       passCount,
			HoursStreamed:   hoursStreamed,
			LiveStream:      liveStream,
		},
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
	// Progressive-trust concurrent-stream cap (D-36): reject over-quota before creating. PRG back to
	// the dashboard with the quota flash.
	if over, _ := overStreamQuota(r.Context(), s.store, s.trust, host, time.Now()); over {
		http.Redirect(w, r, "/app?error=stream-quota", http.StatusSeeOther)
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
		Title: "Edit stream", Nav: "dashboard", Host: hostNav(host),
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
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	// Deleting the LIVE stream must tear down its room too (D-40): the FK cascade drops the
	// sessions row, but the host-scoped room + connected peers would otherwise linger and be
	// reused by the host's next stream. Hold the per-host binding lock across the liveness check,
	// delete, and teardown so a concurrent goLive can't interleave (codex); capture liveness
	// BEFORE the delete (the cascade erases the session row).
	unlock := s.binds.lock(host.ID)
	defer unlock()
	wasLive, _ := s.sessionState(r.Context(), host.ID, st.ID)
	peers := streamPeerIDs(r.Context(), s.store, st.ID) // collect BEFORE the cascade erases the passes
	if err := s.store.DeleteStream(r.Context(), st.ID); err != nil {
		http.Error(w, "could not delete stream", http.StatusInternalServerError)
		return
	}
	teardownDeletedStream(s.hub, host.ID, wasLive, peers)
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
