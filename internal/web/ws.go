// Package web exposes GuestPass's HTTP surface: the signaling WebSocket endpoint
// and (for now) static assets. See docs/ARCHITECTURE.md §6–§7.
package web

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/rock3r/guest-pass/internal/signaling"
)

// wsHandler serves GET /ws, the one signaling WebSocket. Each connection is
// authenticated by credential (session cookie → host, ?pass= → guest/cohost, ?src= →
// OBS source) and the role/peer/session are derived from that credential against live DB
// state, never from a frame body (EN-7/AC-1). The handler relays opaque SDP/ICE between
// peers and never inspects media (D-23).
type wsHandler struct {
	hub        *signaling.Hub
	resolver   *wsResolver
	inflight   *sync.WaitGroup       // nil-safe; lets a graceful drain wait for terminate flush (RF-21)
	iceServers []signaling.ICEServer // ICE config handed to every peer in the {t:"ice"} join-ack (AD-14)
	log        *slog.Logger
}

// newWSHandler builds the handler, defaulting the logger so the hot path never nil-panics.
func newWSHandler(hub *signaling.Hub, resolver *wsResolver, inflight *sync.WaitGroup, ice []signaling.ICEServer, logger *slog.Logger) *wsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &wsHandler{hub: hub, resolver: resolver, inflight: inflight, iceServers: ice, log: logger}
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
		_ = wsjson.Write(ctx, c, signaling.Frame{T: "terminate", Reason: "reconnect"})
		return
	}

	out := make(chan signaling.Frame, 64)
	// Enqueue the ICE join-ack BEFORE Join (AD-14): the buffered channel makes it the
	// first frame the writer flushes, ahead of anything the room emits on join. Skipped
	// when no servers are configured (dev/loopback).
	if len(h.iceServers) > 0 {
		out <- signaling.Frame{T: "ice", ICEServers: h.iceServers}
	}
	if !room.Join(id.peer, id.role, id.slot, out) {
		// The room started draining between hub.Room and Join. Tell the client to
		// reconnect and close; we never registered, so there's no writer to drain.
		_ = wsjson.Write(ctx, c, signaling.Frame{T: "terminate", Reason: "reconnect"})
		return
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
	case "rebind":
		if id.role == "host" {
			room.Rebind(signaling.SlotID(f.Slot), signaling.PeerID(f.OccupantPeerID))
		}
	case "unbind":
		if id.role == "host" {
			room.Unbind(signaling.SlotID(f.Slot))
		}
	case "obs":
		if id.isSource() && f.Event == "sourceActive" {
			// Epoch echoed by the source; the room ignores stale epochs (EN-3).
			room.ObsActive(id.slot, f.Active, f.Epoch)
		}
	}
}
