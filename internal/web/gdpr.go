package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// GDPR host self-service (D-37 / §8 / AC-3..5). The host is the only meaningfully non-anonymous
// data subject GuestPass holds, and the surface is tiny — so full self-service is offered in v1:
//
//   - GET    /api/me/export  — data portability: a single JSON download of the host PII surface
//     (account + their streams + the invited-guest PII they hold). Never includes token hashes.
//   - PATCH  /api/me         — rectification: edit the host's display name (the one in-app-editable
//     field; email/identity come from Google).
//   - DELETE /api/me         — erasure: delete the account + ALL the host's data (host-scoped wipe,
//     store.DeleteHost cascade), REFUSED while a live session exists (D-M5-3) — "end your live
//     stream first". Global anonymous counters have no foreign keys and survive the wipe.
//
// The no-JS settings page (CONVENTIONS §3.1) drives the same operations: an export link (GET), and
// amend/delete POST-redirect-GET forms (HTML forms can't PATCH/DELETE) — appServer.amendSettings /
// deleteSettings below. All routes are host-authenticated (RequireHost, EN-6); none touch another
// host's data (EN-8); nothing here logs PII or tokens (EN-16/EN-20).

const maxHostNameRunes = 100 // a sane cap for the host's display name (rectification input)

// errAccountLive is returned by deleteAccount when the host has a live session, so erasure is
// refused until they end it (D-M5-3).
var errAccountLive = errors.New("account has a live session")

// errAccountLastAdmin is returned by deleteHostAccount when the host is the only active admin, so
// self-erasure would strand the instance with no admin (the AC-9 / D-M5.5-5 invariant). Like the
// live-session guard, erasure is DEFERRED not denied — the host promotes a replacement admin, then
// deletes — so a data subject's right to erasure is preserved (GDPR), it just can't orphan the
// instance.
var errAccountLastAdmin = errors.New("account is the last active admin")

// --- export DTOs (PII surface only — NO token hashes, NO slot/source or TURN secrets) ---

type exportAccount struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	GoogleSub string `json:"google_sub"`
	CreatedAt int64  `json:"created_at"`
}

type exportStream struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	ScheduledAt    *int64 `json:"scheduled_at,omitempty"`
	DurationMin    *int64 `json:"duration_min,omitempty"`
	MaxRes         *int64 `json:"max_res,omitempty"`          // host-set program quality ceiling (D-19)
	MaxFPS         *int64 `json:"max_fps,omitempty"`          // …included so the takeout is complete
	MaxBitrateKbps *int64 `json:"max_bitrate_kbps,omitempty"` // …per "everything we hold about you"
	CreatedAt      int64  `json:"created_at"`
}

type exportGuest struct {
	StreamID string `json:"stream_id"`
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`
	Status   string `json:"status"`
}

// exportPreferences contains every host-owned preference except the encrypted TURN credential.
// It is deliberately a separate DTO so future store fields cannot accidentally enter a takeout.
type exportPreferences struct {
	Timezone               string `json:"timezone"`
	YouTubeChannel         string `json:"youtube_channel,omitempty"`
	TwitchChannel          string `json:"twitch_channel,omitempty"`
	DefaultChannelPlatform string `json:"default_channel_platform,omitempty"`
	MaxRes                 int64  `json:"max_res"`
	MaxFPS                 int64  `json:"max_fps"`
	MaxBitrateKbps         int64  `json:"max_bitrate_kbps"`
	CustomTURNEnabled      bool   `json:"custom_turn_enabled"`
	CustomTURNURL          string `json:"custom_turn_url,omitempty"`
}

type exportData struct {
	Account     exportAccount     `json:"account"`
	Preferences exportPreferences `json:"preferences"`
	Streams     []exportStream    `json:"streams"`
	Guests      []exportGuest     `json:"guests"`
}

// gatherExport reads the host's full PII surface — account, preferences, streams, and the
// invited-guest PII they hold — scoped to the host (EN-8). It deliberately maps into DTOs that
// omit token hashes and the encrypted TURN credential so the takeout never carries a secret.
func gatherExport(ctx context.Context, st *store.Store, host *store.Host) (exportData, error) {
	prefs, err := st.GetHostPreferences(ctx, host.ID)
	if err != nil {
		return exportData{}, err
	}
	out := exportData{
		Account: exportAccount{Email: host.Email, Name: host.Name, GoogleSub: host.GoogleSub, CreatedAt: host.CreatedAt},
		Preferences: exportPreferences{
			Timezone: prefs.Timezone, YouTubeChannel: prefs.YouTubeChannel, TwitchChannel: prefs.TwitchChannel,
			DefaultChannelPlatform: prefs.DefaultChannelPlatform, MaxRes: prefs.MaxRes, MaxFPS: prefs.MaxFPS,
			MaxBitrateKbps: prefs.MaxBitrateKbps, CustomTURNEnabled: prefs.CustomTURNEnabled,
			CustomTURNURL: prefs.CustomTURNURL,
		},
		Streams: []exportStream{},
		Guests:  []exportGuest{},
	}
	streams, err := st.ListStreamsByHost(ctx, host.ID)
	if err != nil {
		return exportData{}, err
	}
	for _, s := range streams {
		out.Streams = append(out.Streams, exportStream{
			ID: s.ID, Title: s.Title, Status: s.Status,
			ScheduledAt: s.ScheduledAt, DurationMin: s.DurationMin,
			MaxRes: s.MaxRes, MaxFPS: s.MaxFPS, MaxBitrateKbps: s.MaxBitrateKbps,
			CreatedAt: s.CreatedAt,
		})
	}
	passes, err := st.ListPassesByHost(ctx, host.ID)
	if err != nil {
		return exportData{}, err
	}
	for _, p := range passes {
		g := exportGuest{StreamID: p.StreamID, Role: p.Role, Status: p.Status}
		if p.Name != nil {
			g.Name = *p.Name
		}
		if p.Email != nil {
			g.Email = *p.Email
		}
		out.Guests = append(out.Guests, g)
	}
	return out, nil
}

// cleanHostName trims, rejects empty, and caps the host's display name (rectification input). It
// returns the cleaned value and false if it is empty after trimming.
func cleanHostName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", false
	}
	if utf8.RuneCountInString(name) > maxHostNameRunes {
		// Cap by runes without splitting a multibyte rune.
		r := []rune(name)
		name = string(r[:maxHostNameRunes])
	}
	return name, true
}

// deleteHostAccount runs the GDPR erasure under the per-host binding lock (so a concurrent goLive
// can't start a session between the live-check and the wipe): it refuses with errAccountLive while
// a live session exists (D-M5-3), else deletes all the host's data (store.DeleteHost cascade) and
// tears down any lingering (pre-live) room. The caller clears the session cookie. It never logs PII
// (EN-16). Shared by the JSON API (deleteMe) and the no-JS settings form (deleteSettings).
func deleteHostAccount(ctx context.Context, st *store.Store, hub *signaling.Hub, binds *bindingLocks, hostID string) error {
	if binds != nil {
		unlock := binds.lock(hostID)
		defer unlock()
	}
	switch _, err := st.ActiveSession(ctx, hostID); {
	case err == nil:
		return errAccountLive // refuse while live — "end your live stream first"
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	// Refuse if this host is the only active admin: erasing it would strand the instance with no
	// admin able to approve/promote hosts (the AC-9 invariant the suspend/demote guard also keeps).
	// Erasure is deferred, not denied — promote a replacement admin first (mirrors the live guard).
	host, err := st.GetHost(ctx, hostID)
	if err != nil {
		return err // the host was authed for this request; a miss here is an infra error, propagate it
	}
	if host.IsAdmin && host.Status == store.HostActive {
		switch n, err := st.CountActiveAdmins(ctx); {
		case err != nil:
			return err
		case n <= 1:
			return errAccountLastAdmin
		}
	}
	// Tear down any pre-live room (greenroom open but never went live) with a TERMINAL reason for
	// EVERY peer including OBS sources — bracketing the wipe on BOTH sides (codex):
	//   • BEFORE DeleteHost: a source connected NOW gets the terminal frame over its existing socket
	//     and stops, before the cascade erases its slot token — so it never reconnect-loops against a
	//     dead token (EndSession's source→reconnect would; TerminateHostRoom is terminal).
	//   • AFTER DeleteHost: sweeps a room that a host/OBS-source WS — authenticated just before the
	//     delete — spawned via hub.Room mid-erase, so account erasure doesn't leave a ghost in-memory
	//     room for the now-deleted host.
	// Both are no-ops when no room is live. (A WS whose handshake completed pre-delete but whose
	// hub.Room lands after this second sweep is a negligible residual: a transient room with no DB
	// backing and no data, cleared on disconnect/restart — RF-27-class boundary edge.)
	if hub != nil {
		hub.TerminateHostRoom(hostID, signaling.TerminateSessionEnded)
	}
	if err := st.DeleteHost(ctx, hostID); err != nil {
		return err
	}
	if hub != nil {
		hub.TerminateHostRoom(hostID, signaling.TerminateSessionEnded)
	}
	return nil
}

// --- JSON API (DESIGN §5) ---

// exportMe serves GET /api/me/export — the host PII takeout as a single JSON attachment (AC-3).
func (a *apiServer) exportMe(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	data, err := gatherExport(r.Context(), a.store, host)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not build export")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="guestpass-export.json"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(data) // header already committed; a write error mid-stream can't be recovered
}

type amendMeRequest struct {
	Name string `json:"name"`
}

// amendMe serves PATCH /api/me — rectify the host's display name (AC-4).
func (a *apiServer) amendMe(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req amendMeRequest
	if !readJSON(w, r, &req) { // bounded body + DisallowUnknownFields, like the rest of the JSON API
		return
	}
	name, ok := cleanHostName(req.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, "name must not be empty")
		return
	}
	if err := a.store.SetHostName(r.Context(), host.ID, name); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// deleteMe serves DELETE /api/me — erase the account + all the host's data (AC-5). Refused (409)
// while a live session exists. On success it clears the session cookie and returns 204.
func (a *apiServer) deleteMe(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch err := deleteHostAccount(r.Context(), a.store, a.hub, a.binds, host.ID); {
	case errors.Is(err, errAccountLive):
		writeError(w, http.StatusConflict, "end your live stream first")
		return
	case errors.Is(err, errAccountLastAdmin):
		writeError(w, http.StatusConflict, "promote another admin before deleting your account")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "could not delete account")
		return
	}
	if a.auth != nil {
		a.auth.ClearSession(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- no-JS settings page forms (POST-redirect-GET; CONVENTIONS §3.1) ---

// amendSettings handles the settings page's "save name" form (POST /app/settings/amend) → the same
// rectification as PATCH /api/me, then redirects back. CSRF-safe via the SameSite=Lax cookie.
func (s *appServer) amendSettings(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	name, valid := cleanHostName(r.FormValue("name"))
	if !valid {
		http.Redirect(w, r, "/app/settings?error=name", http.StatusSeeOther)
		return
	}
	if err := s.store.SetHostName(r.Context(), host.ID, name); err != nil {
		http.Error(w, "could not update account", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/settings?saved=1", http.StatusSeeOther)
}

// deleteSettings handles the settings page's "delete my account" form (POST /app/settings/delete)
// → the same erasure as DELETE /api/me (refused while live), clears the session, and lands on the
// public landing. CSRF-safe via the SameSite=Lax cookie.
func (s *appServer) deleteSettings(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Require the confirmation field SERVER-SIDE (codex): the form's `required` checkbox is only a
	// browser affordance; a scripted or malformed POST must not erase the account without it.
	if r.FormValue("confirm") != "1" {
		http.Redirect(w, r, "/app/settings?error=confirm", http.StatusSeeOther)
		return
	}
	switch err := deleteHostAccount(r.Context(), s.store, s.hub, s.binds, host.ID); {
	case errors.Is(err, errAccountLive):
		http.Redirect(w, r, "/app/settings?error=live", http.StatusSeeOther)
		return
	case errors.Is(err, errAccountLastAdmin):
		http.Redirect(w, r, "/app/settings?error=last-admin", http.StatusSeeOther)
		return
	case err != nil:
		http.Error(w, "could not delete account", http.StatusInternalServerError)
		return
	}
	if s.auth != nil {
		s.auth.ClearSession(w)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
