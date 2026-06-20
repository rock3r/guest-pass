package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// sessionStatus reports whether THIS stream is the host's currently-live session — the read-only
// liveness the no-JS stream-detail page polls (M5.5). The page computes its "● Live" pill once at
// render, so when the session is force-ended out from under it (admin D-27 cascade, idle reaper,
// or an end from another tab) the pill goes stale until a manual refresh; the poll lets the page
// swap the pill for an "ended" notice in place. Host-only (RequireHost), RF-2 same-host via
// ownedStream (a foreign/unknown stream is 404). Read-only — no DB write. A suspended host is
// gated by the middleware before reaching here (403), which the poller treats as "ended" too.
func (s *appServer) sessionStatus(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	live, _ := s.sessionState(r.Context(), host.ID, st.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"live": live})
}

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
	// Hold the per-host binding lock across StartSession AND the room reconcile, so a concurrent
	// endSession (another tab) can't interleave its DB-clear + room-teardown between them (D-20).
	unlock := s.binds.lock(host.ID)
	defer unlock()
	switch _, err := s.store.StartSession(r.Context(), st.ID, host.ID); {
	case errors.Is(err, store.ErrSessionAlreadyLive):
		http.Error(w, "end your current live session before starting another (one live session at a time)", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "could not go live", http.StatusInternalServerError)
		return
	}
	// The session row is committed; the room reconciliation below MUST NOT be abandoned if the
	// host's POST is canceled/disconnects — otherwise a straggler from another stream would linger
	// in the now-live room. Detach from the request's cancellation (keeping its values) with a short
	// timeout (codex).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	// ISOLATION-CRITICAL, fail closed (codex): evict guests that must not be in the now-on-air room
	// — other-stream stragglers admitted pre-live, and same-stream guests whose invite lapsed. If
	// their pass lists can't even be loaded (DB error / timeout), we cannot guarantee the room is
	// clean, so roll the session back and fail rather than go live with peers that can mesh with the
	// show. (The handshake gate only blocks FUTURE joins.)
	if err := s.evictNonSessionPeersLocked(ctx, host.ID, st.ID); err != nil {
		// Roll back on a FRESH context: `ctx` is the very context whose expiry/cancel may have failed
		// the reconcile, so reusing it would cancel EndActiveSession too and leave the session wrongly
		// active (codex). A new detached context with its own budget undoes the commit.
		rbCtx, rbCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer rbCancel()
		_ = s.store.EndActiveSession(rbCtx, host.ID)
		http.Error(w, "could not go live — please try again", http.StatusInternalServerError)
		return
	}
	// Best-effort: replay an already-spawned room's persisted bindings now that the session is live
	// (D-40) — a guest that connected BEFORE Go live had its join-replay gated off and any pre-live
	// picker bind was DB-only. A load failure here is non-isolation (the host just re-picks).
	s.replayBindingsLocked(ctx, host.ID, st.ID)
	// Tell a greenroom that was open BEFORE Go live to drop its optimistic pre-live slot overrides
	// and reconcile to the now-authoritative roster (codex) — sent AFTER the replay so the live
	// bindings land first.
	if s.hub != nil {
		if room := s.hub.RoomIfLive(host.ID); room != nil {
			room.NotifySessionLive()
		}
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
	// Serialize the DB-clear + room-teardown under the per-host binding lock so a concurrent goLive
	// can't slip a StartSession into the gap and attach a new session to the about-to-be-torn-down
	// room (codex). The lock also orders this with /ws joins and picker PUTs (D-20).
	unlock := s.binds.lock(host.ID)
	defer unlock()
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

// replayBindingsLocked binds the stream's persisted cam bindings into the live room (if one
// already exists) the moment the session starts — closing the gap where guests/OBS connected
// before Go live (join-replay gated off, pre-live picker binds DB-only). The CALLER must hold the
// per-host binding lock (goLive does), so it orders with concurrent joins/picker PUTs/endSession;
// ResumeBind is non-displacing and no-ops for a pass whose peer isn't connected, so replaying the
// whole set is safe.
func (s *appServer) replayBindingsLocked(ctx context.Context, hostID, streamID string) {
	if s.hub == nil {
		return
	}
	room := s.hub.RoomIfLive(hostID)
	if room == nil {
		return // nobody connected yet — each guest's own join-replay will bind it (now in-scope)
	}
	bound, err := s.store.BoundCamPassesForStream(ctx, streamID)
	if err != nil {
		return // best-effort: a read miss just means the host re-picks; no live disruption
	}
	for _, b := range bound {
		room.ResumeBind(signaling.SlotID(b.SlotLabel), signaling.PeerID(b.PassID))
	}
}

// streamPeerIDs collects a stream's pass ids (its guests' room peer ids) so a delete can evict any
// that are connected. Read BEFORE the delete, since the FK cascade erases the passes. Best-effort:
// a read miss returns nil (the sockets drop on their own).
func streamPeerIDs(ctx context.Context, st *store.Store, streamID string) []signaling.PeerID {
	passes, err := st.ListPassesByStream(ctx, streamID)
	if err != nil {
		return nil
	}
	out := make([]signaling.PeerID, 0, len(passes))
	for _, p := range passes {
		out = append(out, signaling.PeerID(p.ID))
	}
	return out
}

// teardownDeletedStream removes the deleted stream's live footprint from the host-scoped room so
// nothing carries into the host's next session (D-40): if the deleted stream WAS the live session,
// the whole room is torn down (session-ended); otherwise only that stream's (possibly pre-live)
// guest peers are evicted (revoked), leaving any other stream's live session untouched. Call AFTER
// the delete, with peers collected before it. Host-global slot sources aren't pass peers, so they
// are never caught here.
func teardownDeletedStream(hub *signaling.Hub, hostID string, wasLive bool, peers []signaling.PeerID) {
	if hub == nil {
		return
	}
	if wasLive {
		hub.EndSession(hostID, signaling.TerminateSessionEnded)
		return
	}
	hub.EvictIfLive(hostID, signaling.TerminateRevoked, peers)
}

// evictNonSessionPeersLocked clears the now-on-air room of guests that must not be in it
// (isolation, codex): (1) stragglers from one of the host's OTHER streams, admitted pre-live, get a
// TRANSIENT reconnect — re-handshake then refused by the admission gate ("stream not live"); and
// (2) same-stream guests whose invite lapsed (revoked/expired) while connected pre-live get the
// matching terminal reason (revoked/expired screen). It loads BOTH pass lists FIRST and returns any
// load error so the caller fails closed — it must not evict a partial set and report success. The
// caller holds the per-host binding lock; EvictPeers no-ops ids that aren't connected (and slot
// sources aren't pass ids, so they're never caught).
func (s *appServer) evictNonSessionPeersLocked(ctx context.Context, hostID, liveStreamID string) error {
	if s.hub == nil {
		return nil
	}
	stragglers, err := s.store.OtherStreamPassIDs(ctx, hostID, liveStreamID)
	if err != nil {
		return err
	}
	revoked, expired, err := s.store.RetiredPassIDsForStream(ctx, liveStreamID, time.Now().Unix())
	if err != nil {
		return err
	}
	s.hub.EvictIfLive(hostID, signaling.TerminateReconnect, peerIDs(stragglers))
	s.hub.EvictIfLive(hostID, signaling.TerminateRevoked, peerIDs(revoked))
	s.hub.EvictIfLive(hostID, signaling.TerminateExpired, peerIDs(expired))
	return nil
}

// peerIDs converts pass ids (which are room peer ids) to PeerIDs.
func peerIDs(ids []string) []signaling.PeerID {
	out := make([]signaling.PeerID, 0, len(ids))
	for _, id := range ids {
		out = append(out, signaling.PeerID(id))
	}
	return out
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
