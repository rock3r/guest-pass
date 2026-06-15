// Package web exposes GuestPass's HTTP surface: the signaling WebSocket endpoint
// and (for now) static assets. See docs/ARCHITECTURE.md §6–§7.
package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// kickRevokeTimeout bounds the pass-revocation write that backs a kick (D-25/RF-22), so a wedged
// disk can't stall the room goroutine indefinitely (it is a rare control-plane action).
const kickRevokeTimeout = 5 * time.Second

// ICEConfigurer builds the per-peer {t:"ice"} join-ack (AD-14). The peer id is passed so a
// TURN entry can carry a freshly-minted ephemeral credential bound to that peer (EN-4); ok
// is false when no ICE servers are configured at all (dev/loopback), so the join-ack is
// skipped. *turn.Provider implements it.
type ICEConfigurer interface {
	ICEFrame(peerID string) (signaling.Frame, bool)
}

// wsHandler serves GET /ws, the one signaling WebSocket. Each connection is
// authenticated by credential (session cookie → host, ?pass= → guest/cohost, ?src= →
// OBS source) and the role/peer/session are derived from that credential against live DB
// state, never from a frame body (EN-7/AC-1). The handler relays opaque SDP/ICE between
// peers and never inspects media (D-23).
type wsHandler struct {
	hub      *signaling.Hub
	resolver *wsResolver
	inflight *sync.WaitGroup // nil-safe; lets a graceful drain wait for terminate flush (RF-21)
	ice      ICEConfigurer   // per-peer ICE join-ack (AD-14); nil = no ICE servers offered
	binds    *bindingLocks   // serialize the join-replay with the host's binding PUTs (D-20)
	log      *slog.Logger
}

// newWSHandler builds the handler, defaulting the logger so the hot path never nil-panics.
func newWSHandler(hub *signaling.Hub, resolver *wsResolver, inflight *sync.WaitGroup, ice ICEConfigurer, binds *bindingLocks, logger *slog.Logger) *wsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &wsHandler{hub: hub, resolver: resolver, inflight: inflight, ice: ice, binds: binds, log: logger}
}

func (h *wsHandler) serve(w http.ResponseWriter, r *http.Request) {
	// Count the handler at entry — before the upgrade — so a drain's Wait can't race a
	// handler sitting between Accept and a later Add. Done covers every exit path.
	if h.inflight != nil {
		h.inflight.Add(1)
		defer h.inflight.Done()
	}

	// Authenticate BEFORE the upgrade, so a rejected credential is a clean HTTP status
	// (no half-open socket) and never logs the token (EN-16).
	id, aerr := h.resolver.resolve(r)
	if aerr != nil {
		h.log.Warn("ws handshake rejected",
			"reason", aerr.reason, "status", aerr.status,
			"path", redactWSURL(r.URL), "ip", ClientIP(r))
		http.Error(w, http.StatusText(aerr.status), aerr.status)
		return
	}

	// An OBS browser source (OBS-CEF) may send a literal "null" Origin. Normalize it to
	// absent so the same-origin verification inside websocket.Accept admits it — the slot
	// source token is the credential, so there is no CSRF risk. This relaxation is for
	// source-token connections ONLY (TESTING.md §WS): host/guest connections keep strict
	// Origin validation. No InsecureSkipVerify — the library still verifies Origin.
	if id.isSource() && r.Header.Get("Origin") == "null" {
		r.Header.Del("Origin")
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		// Accept already wrote the response (e.g. 403 on a disallowed Origin).
		return
	}
	defer c.CloseNow()

	ctx := r.Context()
	room := h.hub.Room(id.session)
	if room == nil {
		// The hub is draining; tell the client to reconnect (transient, EN-9) and close.
		_ = wsjson.Write(ctx, c, signaling.Frame{T: "terminate", Reason: signaling.TerminateReconnect})
		return
	}

	// Re-validate a source token right before admitting it to the room (D-22): a panic rotation
	// landing between the handshake and Join would otherwise let a now-dead token slip past the
	// teardown (a resolve→Join TOCTOU). The full close — a per-session media grant gating join by
	// generation — is v1.1 (AD-23/RF-3); this shuts the realistic window (spamming connects across
	// the slow WS upgrade). A genuine rotation tells the source it was replaced so it stops (EN-9).
	if id.isSource() && !h.resolver.sourceStillValid(ctx, r.URL.Query().Get("src")) {
		_ = wsjson.Write(ctx, c, signaling.Frame{T: "terminate", Reason: signaling.TerminateTokenRotated})
		return
	}

	out := make(chan signaling.Frame, 64)
	// Enqueue the ICE join-ack BEFORE Join (AD-14): the buffered channel makes it the first
	// frame the writer flushes, ahead of anything the room emits on join. The config is
	// built per-peer so a TURN entry carries a fresh ephemeral credential (EN-4); skipped
	// when no servers are configured (dev/loopback).
	if h.ice != nil {
		if iceFrame, ok := h.ice.ICEFrame(string(id.peer)); ok {
			out <- iceFrame
		}
	}
	if !room.Join(id.peer, id.role, id.name, id.slot, out) {
		// The room started draining between hub.Room and Join. Tell the client to
		// reconnect and close; we never registered, so there's no writer to drain.
		_ = wsjson.Write(ctx, c, signaling.Frame{T: "terminate", Reason: signaling.TerminateReconnect})
		return
	}

	// Replay a guest's/co-host's persisted cam-slot binding as a live (re)bind (D-20/D-40): now
	// that it is a room peer, Room.ResumeBind re-routes /s/{slot} to it — so a binding made before
	// OBS/guests connected, or surviving a reconnect, takes effect without the host re-binding.
	// The binding is re-read HERE (not at the handshake), so a host PUT during the join window
	// can't replay a stale label. ResumeBind (not Rebind) is NON-displacing: an automatic replay
	// must never knock a different live occupant off a slot. v1 runs one live session per host,
	// but pass.slot_id isn't gated on which stream is live (session lifecycle is v1.1), so a guest
	// of a non-live stream opening their link must not auto-hijack the on-air slot. The host's own
	// greenroom (re)bind still displaces deliberately (putPassSlot → Rebind/RebindOrVacate).
	if id.role == "guest" || id.role == "cohost" {
		// Re-read + enqueue under the per-host binding lock so a concurrent host PUT can't make
		// this replay route from a stale binding (the lock orders the room commands by DB commit).
		unlock := h.binds.lock(id.session)
		if slot := h.resolver.guestBoundSlot(ctx, string(id.peer), id.session); slot != "" {
			room.ResumeBind(slot, id.peer)
		}
		unlock()
	}

	// Single writer goroutine (EN-12): the ONLY place this socket is written. It ends when
	// the room closes out (during Leave below or an eviction/terminate).
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for f := range out {
			if err := wsjson.Write(ctx, c, f); err != nil {
				// Unblock the reader so it breaks and the room Leave runs.
				c.CloseNow()
				return
			}
		}
		// out was closed by the room (Leave / eviction / drain). Close the socket to
		// unblock the reader and let the handler return promptly.
		c.CloseNow()
	}()

	// Reader loop: parse frames → room commands, gated by role (EN-7).
	for {
		var f signaling.Frame
		if err := wsjson.Read(ctx, c, &f); err != nil {
			break
		}
		h.dispatch(room, id, f)
	}

	// Leave closes out (on the room goroutine), which ends the writer. Called here, not via
	// defer, so <-writerDone doesn't deadlock. The out channel identifies THIS connection
	// so a since-evicted duplicate is a no-op.
	room.Leave(id.peer, out)
	<-writerDone
}

// dispatch routes a client frame to the room, enforcing role authority (EN-7): only a
// host (re)binds slots; only an OBS source reflects on-air program state; any peer may
// relay signaling. The role comes from the credential, never the frame.
func (h *wsHandler) dispatch(room *signaling.Room, id wsIdentity, f signaling.Frame) {
	switch f.T {
	case "signal":
		room.Signal(id.peer, f) // relayed verbatim; server never inspects (D-23)
	case "state":
		// Self-presence (EN-7): cam/mic/screen are the sender's OWN modality flags, folded into
		// the roster; level is the audio meter, coalesced onto the {t:levels} tick (AD-13), never
		// the roster. Only a participant has presence — an OBS source reflects on-air, not state.
		// A self-state that re-enables a suppression-locked modality is rejected server-side (EN-7).
		if !id.isSource() {
			room.ApplyState(id.peer, f.Cam, f.Mic, f.Screen, f.Level)
		}
	case "stats":
		// Self-degradation report (AD-21): the publisher's OWN coarse signal/RTT and shedding state,
		// folded into its roster entry so the host sees per-tile health + a degrading/recovering
		// badge. Per-frame stats stay in memory (EN-11). Only a participant publishes media stats.
		if !id.isSource() {
			room.ApplyStats(id.peer, f.Signal, f.RttMs, f.Degraded)
		}
	case "force-mute", "force-no-cam", "force-no-share":
		// Suppressive, authority-locked forces (D-13/EN-7). The actor is the credential's peer;
		// the target is the `peerId` string. Rank authority (strictly-above, demotion-safe) is
		// enforced server-side in the reducer, so a guest's or peer's attempt is a no-op there.
		if !id.isSource() {
			room.Force(id.peer, signaling.PeerID(f.PeerID), forceModality(f.T))
		}
	case "release":
		// Lift a suppression lock (D-13). The target can never self-release; the reducer checks
		// the actor's current rank ≥ the lock floor. Kind is the modality (mic|cam|share).
		if !id.isSource() && isLockModality(f.Kind) {
			room.Release(id.peer, signaling.PeerID(f.PeerID), f.Kind)
		}
	case "role":
		// Promote/demote co-host↔guest (D-15) — HOST-ONLY. The reducer also enforces host
		// authority + target-strictly-below against current rank (demotion-safe), so this gate is
		// convenience; the server stays the sole authority (EN-7).
		if id.role == "host" {
			room.SetRole(id.peer, signaling.PeerID(f.PeerID), f.Role)
		}
	case "recover-quality":
		// Host "bump quality now" (AD-21/D-34): broadcast to every publisher to recover immediately,
		// overriding the slow recover hysteresis. Host-only — a guest/source attempt is a no-op.
		if id.role == "host" {
			room.RecoverQuality()
		}
	case "chat":
		// Backstage chat (EN-20): relayed to participants only, from-stamped. The text is NEVER
		// persisted or logged — do NOT add any log line here referencing f.Text. OBS sources have
		// no chat (EN-13); the reducer also drops a chat from a non-participant.
		if !id.isSource() {
			room.Chat(id.peer, f.Text)
		}
	case "hand":
		// Hand-raise (the "bring me in" nudge). A participant controls its own hand; a host may
		// dismiss another's by addressing a peerId (lower-only). Authority is server-side (EN-7).
		if !id.isSource() {
			room.SetHand(id.peer, signaling.PeerID(f.PeerID), f.Raised)
		}
	case "kick":
		// Kick a participant (D-25). The revoke closure runs INSIDE Room.Kick on the room
		// goroutine, before the teardown evicts the socket, so a reconnect with the now-revoked
		// pass is refused (refuse-rejoin, race-free, RF-22). Rank authority is enforced in the
		// reducer; the closure only runs when the kick is authorized.
		if !id.isSource() {
			target := signaling.PeerID(f.PeerID)
			room.Kick(id.peer, target, func() {
				kctx, cancel := context.WithTimeout(context.Background(), kickRevokeTimeout)
				defer cancel()
				if err := h.resolver.store.SetPassStatus(kctx, string(target), store.PassRevoked); err != nil && !errors.Is(err, store.ErrNotFound) {
					h.log.Error("revoking kicked pass", "target", target, "err", err)
				}
			})
		}
	case "ice-refresh":
		// Re-mint and re-send the ICE config before the TURN credential expires (EN-4).
		// Delivered through the room so the send runs on the room goroutine and can't race
		// this connection's out-channel close.
		if h.ice != nil {
			if iceFrame, ok := h.ice.ICEFrame(string(id.peer)); ok {
				room.DeliverTo(id.peer, iceFrame)
			}
		}
	case "rebind":
		if id.role == "host" {
			room.Rebind(signaling.SlotID(f.Slot), signaling.PeerID(f.OccupantPeerID))
		}
	case "unbind":
		if id.role == "host" {
			room.Unbind(signaling.SlotID(f.Slot))
		}
	case "obs":
		// On-air/broadcast reflection (D-24) comes from OBS source pages only (EN-7).
		if !id.isSource() {
			return
		}
		switch f.Event {
		case "sourceActive":
			// Epoch echoed by the source; the room ignores stale epochs (EN-3). A
			// sourceActive without an epoch can't be resolved to a slot, so it is ignored.
			if f.Epoch != nil {
				room.ObsActive(id.slot, f.Active, *f.Epoch)
			}
		case "streamingStarted":
			room.ObsStreaming(true)
		case "streamingStopped":
			room.ObsStreaming(false)
		}
	}
}

// forceModality maps a force frame type to the lock modality it suppresses (D-13).
func forceModality(t string) string {
	switch t {
	case "force-mute":
		return "mic"
	case "force-no-cam":
		return "cam"
	case "force-no-share":
		return "share"
	}
	return ""
}

// isLockModality reports whether a {t:release} kind names a real suppression modality, so a
// malformed release is dropped rather than acted on.
func isLockModality(kind string) bool {
	return kind == "mic" || kind == "cam" || kind == "share"
}
