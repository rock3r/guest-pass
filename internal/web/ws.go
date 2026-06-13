// Package web exposes GuestPass's HTTP surface: the signaling WebSocket endpoint
// and (for now) static assets. See docs/ARCHITECTURE.md §6–§7.
package web

import (
	"net/http"
	"sync"

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
//
// inflight (nil-safe) tracks each upgraded connection so a graceful drain can wait for
// terminate frames to flush before the process exits (RF-21).
func ServeWS(hub *signaling.Hub, inflight *sync.WaitGroup) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Count the handler at entry — before the upgrade — so a drain's Wait can't race
		// a handler sitting between Accept and a later Add (which would be an Add-from-zero
		// concurrent with Wait). Done covers every exit path, including validation/Accept
		// failure.
		if inflight != nil {
			inflight.Add(1)
			defer inflight.Done()
		}

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
		if room == nil {
			// The hub is draining (Hub.Shutdown ran). Tell the client to reconnect
			// (transient, EN-9) and close; never spawn a room on a shutting-down hub.
			_ = wsjson.Write(ctx, c, signaling.Frame{T: "terminate", Reason: "reconnect"})
			return
		}
		pid := signaling.PeerID(peer)
		out := make(chan signaling.Frame, 64)
		if !room.Join(pid, role, signaling.SlotID(slot), out) {
			// The room started draining between hub.Room and Join. Tell the client to
			// reconnect and close; we never registered, so there's no writer to drain.
			_ = wsjson.Write(ctx, c, signaling.Frame{T: "terminate", Reason: "reconnect"})
			return
		}

		// Single writer goroutine (EN-12): the ONLY place this socket is written. It
		// ends when the room closes out (during Leave below).
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			for f := range out {
				if err := wsjson.Write(ctx, c, f); err != nil {
					// Unblock the reader so it breaks and the room Leave runs;
					// otherwise the peer stays registered with no drainer.
					c.CloseNow()
					return
				}
			}
			// out was closed by the room (Leave / drain Terminate). All queued frames —
			// including a terminate sent during a graceful drain — are now flushed, so
			// close the socket to unblock the reader and let the handler return promptly.
			c.CloseNow()
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
		// call it here, not via defer, so <-writerDone doesn't deadlock. The out
		// channel identifies THIS connection so a since-evicted duplicate is a no-op.
		room.Leave(pid, out)
		<-writerDone
	}
}
