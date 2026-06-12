// Package web exposes GuestPass's HTTP surface: the signaling WebSocket endpoint
// and (for now) static assets. See docs/ARCHITECTURE.md §6–§7.
package web

import (
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/rock3r/guest-pass/internal/signaling"
)

// ServeWS handles GET /ws.
//
// SPIKE-2 scope: peer identity, role, slot and session arrive as query params
// (session, peer, role, slot). Real token auth (?pass / ?src), strict Origin
// handling incl. the null OBS-CEF Origin, and token redaction (EN-16) land in M2
// step 1. The role is still inferred from the connection, never trusted from a
// frame body (EN-7).
func ServeWS(hub *signaling.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		session, peer := q.Get("session"), q.Get("peer")
		role, slot := q.Get("role"), q.Get("slot")
		if session == "" || peer == "" {
			http.Error(w, "missing session/peer", http.StatusBadRequest)
			return
		}

		// localhost spike: skip Origin verification (M2 implements EN-16).
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()

		ctx := r.Context()
		room := hub.Room(session)
		pid := signaling.PeerID(peer)
		out := make(chan signaling.Frame, 64)
		room.Join(pid, role, signaling.SlotID(slot), out)

		// Single writer goroutine (EN-12): the ONLY place this socket is written. It
		// ends when the room closes out (during Leave below).
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			for f := range out {
				if err := wsjson.Write(ctx, c, f); err != nil {
					return
				}
			}
		}()

		// Reader loop: parse frames → room commands.
		for {
			var f signaling.Frame
			if err := wsjson.Read(ctx, c, &f); err != nil {
				break
			}
			switch f.T {
			case "signal":
				room.Signal(pid, f) // relayed verbatim; server never inspects (D-23)
			case "rebind":
				room.Rebind(signaling.SlotID(f.Slot), signaling.PeerID(f.OccupantPeerID))
			case "unbind":
				room.Unbind(signaling.SlotID(f.Slot))
			case "obs":
				if f.Event == "sourceActive" {
					// Epoch echoed by the source; the room ignores stale epochs (EN-3).
					room.ObsActive(signaling.SlotID(slot), f.Active, f.Epoch)
				}
			}
		}

		// Leave closes out (on the room goroutine), which ends the writer. We must
		// call it here, not via defer, so <-writerDone doesn't deadlock.
		room.Leave(pid)
		<-writerDone
	}
}
