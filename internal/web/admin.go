package web

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// formatInstant renders a unix-seconds timestamp as an absolute UTC string for the admin tables
// (the columns are always present, unlike the optional scheduled_at handled by formatSchedule).
func formatInstant(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("Mon Jan 2, 2006 15:04 UTC")
}

// adminServer backs the read-only, metadata-only admin console (AC-9 / D-14). Admin is an is_admin
// flag on a host, not a separate identity; every route is mounted behind RequireAdmin (live is_admin,
// EN-6). The §7.7 privacy boundary is structural: these handlers read only host/session/stream
// METADATA + in-memory participant counts — they never read passes (guest PII) and never join a room
// as a media/chat peer. PR-8 adds the mutating actions (approve/suspend/promote) on top.
type adminServer struct {
	store *store.Store
	hub   *signaling.Hub // in-memory live participant counts + suspend-cascade force-end (nil in minimal config)
	binds *bindingLocks  // serialize an admin's suspend-cascade force-end with the target's go-live/join (D-27)
	rd    *renderer
}

// --- view models (metadata only; no google_sub, no guest PII, no tokens) ---

type adminSessionView struct {
	SessionID    string `json:"session_id"`
	HostID       string `json:"host_id"`
	HostName     string `json:"host_name"`
	HostEmail    string `json:"host_email"`
	StreamID     string `json:"stream_id"`
	StreamTitle  string `json:"stream_title"`
	StartedAt    int64  `json:"started_at"`
	Participants int    `json:"participants"`
}

type adminHostView struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt int64  `json:"created_at"`
}

type turnRelayView struct {
	Total     int64   `json:"total"`
	Relayed   int64   `json:"relayed"`
	Available bool    `json:"available"`         // false until peer-connection recording exists → render "n/a"
	Percent   float64 `json:"percent,omitempty"` // relayed/total*100, rounded; meaningful only when Available
}

type adminStatsView struct {
	LiveSessions int           `json:"live_sessions"`
	LivePeers    int           `json:"live_peers"`
	TurnRelay    turnRelayView `json:"turn_relay"`
}

// gatherSessions loads the cross-host live sessions and attaches each one's live participant count
// from the hub (in-memory; authoritative for "now"). Metadata only — never reads passes/peers.
func (s *adminServer) gatherSessions(r *http.Request) ([]adminSessionView, int, error) {
	rows, err := s.store.ListActiveSessions(r.Context())
	if err != nil {
		return nil, 0, err
	}
	out := make([]adminSessionView, 0, len(rows))
	totalPeers := 0
	for _, a := range rows {
		n := 0
		if s.hub != nil {
			n = s.hub.ParticipantCount(a.SessionID)
		}
		totalPeers += n
		out = append(out, adminSessionView{
			SessionID: a.SessionID, HostID: a.HostID, HostName: a.HostName, HostEmail: a.HostEmail,
			StreamID: a.StreamID, StreamTitle: a.StreamTitle, StartedAt: a.StartedAt, Participants: n,
		})
	}
	return out, totalPeers, nil
}

func (s *adminServer) gatherStats(r *http.Request) (adminStatsView, error) {
	sessions, peers, err := s.gatherSessions(r)
	if err != nil {
		return adminStatsView{}, err
	}
	total, relayed, err := s.store.TurnRelayStats(r.Context())
	if err != nil {
		return adminStatsView{}, err
	}
	tr := turnRelayView{Total: total, Relayed: relayed, Available: total > 0}
	if tr.Available {
		tr.Percent = math.Round(float64(relayed)/float64(total)*1000) / 10 // one decimal place
	}
	return adminStatsView{LiveSessions: len(sessions), LivePeers: peers, TurnRelay: tr}, nil
}

func (s *adminServer) gatherHosts(r *http.Request) ([]adminHostView, error) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]adminHostView, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, adminHostView{
			ID: h.ID, Email: h.Email, Name: h.Name, Status: h.Status, IsAdmin: h.IsAdmin, CreatedAt: h.CreatedAt,
		})
	}
	return out, nil
}

// --- JSON endpoints (poll/refresh; design §5) ---

func (s *adminServer) statsJSON(w http.ResponseWriter, r *http.Request) {
	stats, err := s.gatherStats(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *adminServer) sessionsJSON(w http.ResponseWriter, r *http.Request) {
	sessions, _, err := s.gatherSessions(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load sessions")
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *adminServer) hostsJSON(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.gatherHosts(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load hosts")
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

// --- server-rendered console page (AC-9) ---

// adminSessionRow / adminHostRow are display-ready (formatted timestamps) variants for the template.
type adminSessionRow struct {
	HostName     string
	HostEmail    string
	StreamTitle  string
	Started      string
	Participants int
}

type adminHostRow struct {
	ID      string
	Email   string
	Name    string
	Status  string
	IsAdmin bool
	Created string
	Live    bool // currently has a live session — drives the suspend "+ end live now" cascade option (D-27)
	Self    bool // this is the acting admin's own row — self-suspend/self-demote are blocked (no lockout)
}

// adminReportRow / adminReportGroup render the abuse reports grouped per reported host (D-42). The
// reporter identity (email) shows ONLY here, on the admin console — never to the reported host.
type adminReportRow struct {
	Reporter string // reporter email, or "(anonymized)" once the retention window has passed
	Category string // human label
	Message  string // free text, or "(anonymized)"
	Stream   string // stream title, or "(deleted)"
	When     string
}

type adminReportGroup struct {
	HostID   string
	HostName string
	Count    int
	Reports  []adminReportRow
}

type adminData struct {
	LiveSessions int
	LivePeers    int
	RelayLabel   string // "12.5%" or "n/a" when no connection data has been recorded yet
	Sessions     []adminSessionRow
	Hosts        []adminHostRow
	Reports      []adminReportGroup
	Flash        string // PRG result banner (e.g. "Host suspended.")
	FlashError   string // PRG error banner (e.g. "You can't suspend your own account.")
}

// labelForReportCategory maps a stored category to its human label (falls back to the raw value).
func labelForReportCategory(c string) string {
	for _, opt := range reportCategoryOptions {
		if opt.Value == c {
			return opt.Label
		}
	}
	return c
}

// gatherReports loads the abuse reports and groups them per reported host (newest-first within each),
// resolving the human labels and the anonymized placeholders for display (D-42 / AC-11).
func (s *adminServer) gatherReports(r *http.Request) ([]adminReportGroup, error) {
	rows, err := s.store.ListReports(r.Context())
	if err != nil {
		return nil, err
	}
	var groups []adminReportGroup
	index := map[string]int{} // host id → position in groups (ListReports is ordered by host)
	for _, row := range rows {
		i, ok := index[row.HostID]
		if !ok {
			groups = append(groups, adminReportGroup{HostID: row.HostID, HostName: row.HostName})
			i = len(groups) - 1
			index[row.HostID] = i
		}
		reporter, message, stream := "(anonymized)", "(anonymized)", "(deleted)"
		if row.ReporterEmail != nil {
			reporter = *row.ReporterEmail
		}
		if row.Message != nil {
			message = *row.Message
		}
		if row.StreamTitle != nil {
			stream = *row.StreamTitle
		}
		groups[i].Reports = append(groups[i].Reports, adminReportRow{
			Reporter: reporter, Category: labelForReportCategory(row.Category),
			Message: message, Stream: stream, When: formatInstant(row.CreatedAt),
		})
		groups[i].Count++
	}
	return groups, nil
}

// adminConsole renders the read-only admin console (AC-9): a metadata-only snapshot of cross-host
// live sessions + participant counts + the anonymous TURN-relay aggregate + the hosts list. No guest
// PII, no media, no chat (§7.7). Host-only + live is_admin (RequireAdmin upstream).
func (s *adminServer) adminConsole(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	stats, err := s.gatherStats(r)
	if err != nil {
		http.Error(w, "could not load admin console", http.StatusInternalServerError)
		return
	}
	sessions, _, err := s.gatherSessions(r)
	if err != nil {
		http.Error(w, "could not load admin console", http.StatusInternalServerError)
		return
	}
	hosts, err := s.gatherHosts(r)
	if err != nil {
		http.Error(w, "could not load admin console", http.StatusInternalServerError)
		return
	}
	reports, err := s.gatherReports(r)
	if err != nil {
		http.Error(w, "could not load admin console", http.StatusInternalServerError)
		return
	}

	d := adminData{LiveSessions: stats.LiveSessions, LivePeers: stats.LivePeers, RelayLabel: "n/a", Reports: reports}
	if stats.TurnRelay.Available {
		d.RelayLabel = strconv.FormatFloat(stats.TurnRelay.Percent, 'f', 1, 64) + "%"
	}
	live := map[string]bool{} // host ids with a live session, for the suspend cascade option
	for _, se := range sessions {
		live[se.HostID] = true
		d.Sessions = append(d.Sessions, adminSessionRow{
			HostName: se.HostName, HostEmail: se.HostEmail, StreamTitle: se.StreamTitle,
			Started: formatInstant(se.StartedAt), Participants: se.Participants,
		})
	}
	for _, h := range hosts {
		d.Hosts = append(d.Hosts, adminHostRow{
			ID: h.ID, Email: h.Email, Name: h.Name, Status: h.Status, IsAdmin: h.IsAdmin,
			Created: formatInstant(h.CreatedAt), Live: live[h.ID], Self: h.ID == host.ID,
		})
	}
	d.Flash, d.FlashError = adminFlash(r.URL.Query().Get("msg")), adminFlash(r.URL.Query().Get("error"))
	s.rd.render(w, r, "admin.html", pageData{
		Title: "Admin", Nav: "admin", Host: &navHost{Name: host.Name, IsAdmin: host.IsAdmin},
		Data: d,
	})
}

// adminFlash maps a PRG flash code to human copy (whitelist; an unknown/empty code → "").
func adminFlash(code string) string {
	switch code {
	case "approved":
		return "Host approved — they can host again."
	case "suspended":
		return "Host suspended — they can't start new streams."
	case "suspended-ended":
		return "Host suspended and their live session was ended."
	case "suspend-end-failed":
		return "Host suspended, but ending their live session failed — try again."
	case "promoted":
		return "Host promoted to admin."
	case "demoted":
		return "Admin demoted to host."
	case "dismissed":
		return "Reports dismissed."
	case "self":
		return "You can't suspend or demote your own account."
	case "notfound":
		return "That host no longer exists."
	default:
		return ""
	}
}
