package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// goLive opens the host's live session for this stream (EN-2/D-20). This is the runtime
// declaration of "which of my streams is live"; it gates the /ws join-replay so only this
// stream's guests auto-bind into the host-global slot pool (a guest of a non-live stream opening
// their link can't hijack the on-air slots). One live session per host: going live while another
// stream is already live is rejected (409) — the host ends that one first. POST-redirect-GET,
// host-only (RequireHost), RF-2 ownership via ownedStream.
func (s *appServer) goLive(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	switch _, err := s.store.StartSession(r.Context(), st.ID, host.ID); {
	case errors.Is(err, store.ErrSessionAlreadyLive):
		http.Error(w, "end your current live session before starting another (one live session at a time)", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "could not go live", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/streams/"+st.ID, http.StatusSeeOther)
}

// endSession ends the host's live session when THIS stream is the live one. It is a no-op (still
// a clean redirect) when the host is not live, or live for a different stream — so a stale page
// can't end the wrong show. POST-redirect-GET, host-only.
func (s *appServer) endSession(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	if live, _ := s.sessionState(r.Context(), host.ID, st.ID); live {
		if err := s.store.EndActiveSession(r.Context(), host.ID); err != nil {
			http.Error(w, "could not end session", http.StatusInternalServerError)
			return
		}
		// Tear down the live room so connected guests/OBS sources get the terminal session-ended
		// teardown and no connection carries into the next stream — rooms are keyed by host id
		// (D-40). No-op when nothing is connected.
		if s.hub != nil {
			s.hub.EndSession(host.ID, signaling.TerminateSessionEnded)
		}
	}
	http.Redirect(w, r, "/app/streams/"+st.ID, http.StatusSeeOther)
}

// sessionState reports whether streamID is the host's live session, and whether the host is live
// for some OTHER stream. A read miss (not live) or transient error both surface as "not live" so
// the page falls back to offering "Go live" rather than blocking on a degraded read.
func (s *appServer) sessionState(ctx context.Context, hostID, streamID string) (thisLive, otherLive bool) {
	sess, err := s.store.ActiveSession(ctx, hostID)
	if err != nil {
		return false, false
	}
	if sess.StreamID == streamID {
		return true, false
	}
	return false, true
}
