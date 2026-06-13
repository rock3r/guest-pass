package signaling

import (
	"context"
	"time"
)

// peerConn is the room goroutine's handle to a connected peer. Frames are delivered
// by a NON-blocking send to out, so a slow/stalled peer never blocks the room
// (AD-12 — drop slow peers); the transport owns the goroutine draining out.
type peerConn struct {
	id   PeerID
	role string
	slot SlotID // for obs source pages: the slot this conn sources ("" otherwise)
	out  chan<- Frame
}

// roomCmd is a closure run on the room goroutine with exclusive access to the pure
// state and the connection table. This is the actor's command channel payload (AD-2):
// all mutation funnels through here, so neither map needs a lock.
type roomCmd func(*roomState, map[PeerID]*peerConn)

// Room is a single live session's actor: one goroutine owns roomState and the conn
// table; every mutation arrives as a roomCmd. No locks on room state.
type Room struct {
	id   string
	cmds chan roomCmd
	done chan struct{}
}

func newRoom(id string) *Room {
	return &Room{id: id, cmds: make(chan roomCmd, 64), done: make(chan struct{})}
}

func (r *Room) run() {
	state := newRoomState()
	conns := map[PeerID]*peerConn{}
	for {
		select {
		case cmd := <-r.cmds:
			cmd(state, conns)
		case <-r.done:
			return
		}
	}
}

func (r *Room) post(cmd roomCmd) {
	select {
	case r.cmds <- cmd:
	case <-r.done:
	}
}

// deliver routes reducer outbounds to peers' send channels, non-blocking (AD-12).
// Runs on the room goroutine, so conns access is race-free.
func deliver(conns map[PeerID]*peerConn, outs []outbound) {
	for _, o := range outs {
		c := conns[o.to]
		if c == nil {
			continue
		}
		select {
		case c.out <- o.frame:
		default:
			// Slow peer: drop the frame (AD-12). Peer eviction lands in M2/M3.
		}
	}
}

// Join registers a connection and enters it into the room. An OBS source page
// (role obs/obs_screen) also subscribes to its slot and is told the current binding.
//
// One connection per identity (EN-16): if a peer id is already connected, the prior
// connection is evicted (its out channel closed) before the new one is installed, so
// a duplicate id can't leave a stale conn that a later Leave would mis-target.
func (r *Room) Join(id PeerID, role string, slot SlotID, out chan<- Frame) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		if old := conns[id]; old != nil {
			// Tell the evicted client to reconnect (EN-9 transient) before closing
			// its channel, so a duplicate identity is a clean handover.
			select {
			case old.out <- Frame{T: "terminate", Reason: "reconnect"}:
			default:
			}
			close(old.out)
		}
		conns[id] = &peerConn{id: id, role: role, slot: slot, out: out}
		outs := st.join(id, role)
		if role == "obs" || role == "obs_screen" {
			outs = append(outs, st.attachSource(slot, id)...)
		}
		deliver(conns, outs)
	})
}

// Leave removes a peer and CLOSES its out channel from the room goroutine — the only
// place that sends to it, so a send can never race a close. It is identity-checked by
// the out channel: a stale/evicted connection (a duplicate id that was already
// replaced) is a no-op, so it never tears down the connection that supplanted it.
func (r *Room) Leave(id PeerID, out chan<- Frame) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		c := conns[id]
		if c == nil || c.out != out {
			return // not the current connection for this id; leave the live one alone
		}
		outs := st.leave(id)
		delete(conns, id)
		deliver(conns, outs)
		close(c.out)
	})
}

func (r *Room) Signal(from PeerID, f Frame) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.relaySignal(from, f))
	})
}

func (r *Room) Rebind(slot SlotID, occupant PeerID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.rebindSlot(slot, occupant))
	})
}

func (r *Room) Unbind(slot SlotID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.unbindSlot(slot))
	})
}

func (r *Room) ObsActive(slot SlotID, active bool, epoch int) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.obsSourceActive(slot, active, epoch))
	})
}

// terminateBudget bounds how long Terminate will wait, across all peers, to enqueue
// terminate frames into backed-up out queues during a drain.
const terminateBudget = 2 * time.Second

// Terminate sends a terminate frame to every connected peer, then closes their out
// channels and clears the roster. Used for graceful shutdown (RF-21): the transient
// reason "reconnect" tells clients to retry with backoff (keyed by pass_id), so a
// deploy/restart isn't a hard mass-drop.
//
// terminate is a terminal control frame, so it must not be silently dropped on a full
// queue (RF-16). The send therefore BLOCKS until the conn's writeLoop drains a slot,
// bounded by a shared deadline so a single wedged socket can't stall the drain — a
// genuinely stuck peer is given up on (its socket is dead anyway) and still closed. It
// runs on the room goroutine, so a concurrent readLoop Leave for a now-removed conn is a
// no-op (identity-checked).
func (r *Room) Terminate(reason string) {
	done := make(chan struct{})
	r.post(func(_ *roomState, conns map[PeerID]*peerConn) {
		ctx, cancel := context.WithTimeout(context.Background(), terminateBudget)
		defer cancel()
		for id, c := range conns {
			select {
			case c.out <- Frame{T: "terminate", Reason: reason}:
			case <-ctx.Done(): // budget exhausted by wedged peers; stop waiting
			}
			close(c.out)
			delete(conns, id)
		}
		close(done)
	})
	select {
	case <-done:
	case <-r.done:
	}
}

// Close stops the room goroutine.
func (r *Room) Close() { close(r.done) }
