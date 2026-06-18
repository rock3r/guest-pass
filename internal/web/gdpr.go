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
//     stream first". No anonymous counters exist yet to preserve (DEF-COUNTERS).
//
// The no-JS settings page (CONVENTIONS §3.1) drives the same operations: an export link (GET), and
// amend/delete POST-redirect-GET forms (HTML forms can't PATCH/DELETE) — appServer.amendSettings /
// deleteSettings below. All routes are host-authenticated (RequireHost, EN-6); none touch another
// host's data (EN-8); nothing here logs PII or tokens (EN-16/EN-20).

const maxHostNameRunes = 100 // a sane cap for the host's display name (rectification input)

// errAccountLive is returned by deleteAccount when the host has a live session, so erasure is
// refused until they end it (D-M5-3).
var errAccountLive = errors.New("account has a live session")

// --- export DTOs (PII surface only — NO token hashes, NO slot/source secrets) ---

type exportAccount struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	GoogleSub string `json:"google_sub"`
	CreatedAt int64  `json:"created_at"`
}

type exportStream struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	ScheduledAt *int64 `json:"scheduled_at,omitempty"`
	DurationMin *int64 `json:"duration_min,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

type exportGuest struct {
	StreamID string `json:"stream_id"`
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`
	Status   string `json:"status"`
}

type exportData struct {
	Account exportAccount  `json:"account"`
	Streams []exportStream `json:"streams"`
	Guests  []exportGuest  `json:"guests"`
}

// gatherExport reads the host's full PII surface — account, their streams, and the invited-guest
// PII they hold — scoped to the host (EN-8). It deliberately maps into DTOs that omit every token
// hash so the takeout never carries a credential.
func gatherExport(ctx context.Context, st *store.Store, host *store.Host) (exportData, error) {
	out := exportData{
		Account: exportAccount{Email: host.Email, Name: host.Name, GoogleSub: host.GoogleSub, CreatedAt: host.CreatedAt},
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
			ScheduledAt: s.ScheduledAt, DurationMin: s.DurationMin, CreatedAt: s.CreatedAt,
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
	// Tear down any lingering pre-live room (greenroom open but never went live) FIRST, with a
	// TERMINAL reason for EVERY peer including OBS sources — BEFORE the cascade erases the slot
	// tokens. EndSession would give sources a recoverable reconnect, so after the tokens vanish they
	// would reconnect-loop against a dead token (codex); TerminateHostRoom stops them for good. No-op
	// when no room is live.
	if hub != nil {
		hub.TerminateHostRoom(hostID, signaling.TerminateSessionEnded)
	}
	if err := st.DeleteHost(ctx, hostID); err != nil {
		return err
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
	case err != nil:
		http.Error(w, "could not delete account", http.StatusInternalServerError)
		return
	}
	if s.auth != nil {
		s.auth.ClearSession(w)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
