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
	// Reconcile an already-spawned room with the persisted bindings now that the session is live
	// (D-40): a guest that connected BEFORE Go live had its join-replay gated off and any pre-live
	// picker bind was DB-only, so bind them now. Under the per-host binding lock to order with
	// concurrent joins/PUTs; ResumeBind is non-displacing and no-ops for a peer that isn't connected.
	s.replayBindingsOnGoLive(r.Context(), host.ID, st.ID)
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

// replayBindingsOnGoLive binds the stream's persisted cam bindings into the live room (if one
// already exists) the moment the session starts — closing the gap where guests/OBS connected
// before Go live (join-replay gated off, pre-live picker binds DB-only). Held under the per-host
// binding lock so it orders with concurrent joins/picker PUTs; ResumeBind is non-displacing and
// no-ops for a pass whose peer isn't connected, so replaying the whole set is safe.
func (s *appServer) replayBindingsOnGoLive(ctx context.Context, hostID, streamID string) {
	if s.hub == nil {
		return
	}
	room := s.hub.RoomIfLive(hostID)
	if room == nil {
		return // nobody connected yet — each guest's own join-replay will bind it (now in-scope)
	}
	unlock := s.binds.lock(hostID)
	defer unlock()
	bound, err := s.store.BoundCamPassesForStream(ctx, streamID)
	if err != nil {
		return // best-effort: a read miss just means the host re-picks; no live disruption
	}
	for _, b := range bound {
		room.ResumeBind(signaling.SlotID(b.SlotLabel), signaling.PeerID(b.PassID))
	}
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
