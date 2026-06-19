package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// Admin mutating actions (AC-10 / D-27 / D-28). All are mounted behind RequireAdmin and are
// server-rendered form POSTs that redirect back to /admin (PRG; CSRF-safe via the SameSite cookie).
// Authority is the live is_admin gate (EN-6); the only authority a route adds on top is the
// self-action guard below. Every status/role change is read live by the authz middleware on the
// target's next request, so suspend/approve/demote take effect mid-session.

// redirectAdmin sends a PRG redirect to the console with a whitelisted flash code (msg|error).
func (s *adminServer) redirectAdmin(w http.ResponseWriter, r *http.Request, key, code string) {
	http.Redirect(w, r, "/admin?"+key+"="+code, http.StatusSeeOther)
}

// adminTarget resolves the {id} target host and the acting admin. A missing target redirects with
// ?error=notfound (a deleted host is not an error condition for the admin). Returns ok=false after
// it has written the response.
func (s *adminServer) adminTarget(w http.ResponseWriter, r *http.Request) (acting, target *store.Host, ok bool) {
	acting, _ = auth.HostFromContext(r.Context()) // RequireAdmin guarantees an admin host in context
	target, err := s.store.GetHost(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		s.redirectAdmin(w, r, "error", "notfound")
		return nil, nil, false
	}
	if err != nil {
		http.Error(w, "could not load host", http.StatusInternalServerError)
		return nil, nil, false
	}
	return acting, target, true
}

// approveHost activates a host — the D-28 pending-host approval (and the reinstatement of a
// previously-suspended host); idempotent on an already-active host.
func (s *adminServer) approveHost(w http.ResponseWriter, r *http.Request) {
	_, target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if err := s.store.SetHostStatus(r.Context(), target.ID, store.HostActive); err != nil {
		http.Error(w, "could not update host", http.StatusInternalServerError)
		return
	}
	s.redirectAdmin(w, r, "msg", "approved")
}

// suspendHost blocks a host from starting future streams (status=suspended, read live by the authz
// middleware, EN-6). With end_live=1 it ALSO runs the D-27 cascade: force-ends the target's running
// live session now (a cooperative teardown + reconnect block, D-25). An admin cannot suspend their
// own account (no self-lockout).
func (s *adminServer) suspendHost(w http.ResponseWriter, r *http.Request) {
	acting, target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if target.ID == acting.ID {
		s.redirectAdmin(w, r, "error", "self")
		return
	}
	if err := s.store.SetHostStatus(r.Context(), target.ID, store.HostSuspended); err != nil {
		http.Error(w, "could not update host", http.StatusInternalServerError)
		return
	}
	msg := "suspended"
	if r.FormValue("end_live") == "1" {
		if err := s.forceEndLive(r.Context(), target.ID); err != nil {
			// The host IS suspended (status persisted, so reconnection is already blocked), but closing
			// the running session failed. Report the partial result rather than claiming it ended — and
			// leave the room up, so we don't desync the live room from a still-active DB session row.
			s.redirectAdmin(w, r, "error", "suspend-end-failed")
			return
		}
		msg = "suspended-ended"
	}
	s.redirectAdmin(w, r, "msg", msg)
}

// forceEndLive ends the target host's live session (D-27 cascade): the DB session row is closed
// (starting the 24h guest-PII purge clock, D-40/D-37) and the in-memory room is terminated for every
// peer including OBS sources (TerminateHostRoom — the suspended host has no next session to reconnect
// to). The per-host binding lock serializes this with the target's own go-live / WS-join replay so an
// in-flight goLive can't outlive the suspend. Both steps are idempotent (no active session → no-op).
//
// It closes the DB row BEFORE tearing down the room (mirroring the canonical host endSession) and
// surfaces a DB-end failure instead of swallowing it — so we never tear down a live room while
// leaving a still-active session row behind (which would strand the purge clock + idle reaper).
func (s *adminServer) forceEndLive(ctx context.Context, hostID string) error {
	var unlock func()
	if s.binds != nil {
		unlock = s.binds.lock(hostID)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	if err := s.store.EndActiveSession(ctx, hostID); err != nil {
		return err
	}
	if s.hub != nil {
		s.hub.TerminateHostRoom(hostID, signaling.TerminateSessionEnded)
	}
	return nil
}

// promoteHost grants is_admin (D-14); idempotent on an existing admin.
func (s *adminServer) promoteHost(w http.ResponseWriter, r *http.Request) {
	_, target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if err := s.store.SetHostAdmin(r.Context(), target.ID, true); err != nil {
		http.Error(w, "could not update host", http.StatusInternalServerError)
		return
	}
	s.redirectAdmin(w, r, "msg", "promoted")
}

// demoteHost clears is_admin (D-14). An admin cannot demote their own account (no self-lockout).
func (s *adminServer) demoteHost(w http.ResponseWriter, r *http.Request) {
	acting, target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if target.ID == acting.ID {
		s.redirectAdmin(w, r, "error", "self")
		return
	}
	if err := s.store.SetHostAdmin(r.Context(), target.ID, false); err != nil {
		http.Error(w, "could not update host", http.StatusInternalServerError)
		return
	}
	s.redirectAdmin(w, r, "msg", "demoted")
}
