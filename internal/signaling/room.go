package signaling

import (
	"sync"
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

// Join registers a connection and enters it into the room, returning whether it was
// admitted. An OBS source page (role obs/obs_screen) also subscribes to its slot and is
// told the current binding.
//
// One connection per identity (EN-16): if a peer id is already connected, the prior
// connection is evicted (its out channel closed) before the new one is installed, so
// a duplicate id can't leave a stale conn that a later Leave would mis-target.
//
// Join returns false (admitting nothing) if the room is draining (Terminate ran) or its
// goroutine has stopped — so a connection that resolved this room just before a shutdown
// can't slip in after the terminate broadcast and strand itself with no teardown. The
// caller then closes the connection itself. Join is synchronous: it waits for the
// command to run, so the result reflects the room's actual state.
func (r *Room) Join(id PeerID, role string, slot SlotID, out chan<- Frame) bool {
	admitted := make(chan bool, 1)
	cmd := func(st *roomState, conns map[PeerID]*peerConn) {
		if st.terminating {
			admitted <- false
			return
		}
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
		admitted <- true
	}
	select {
	case r.cmds <- cmd:
		// The command was enqueued, but the room goroutine may still exit on r.done
		// (Close) before running it — so wait on both, never just <-admitted, or a
		// Close racing the enqueue would block Join forever.
		select {
		case ok := <-admitted:
			return ok
		case <-r.done:
			return false
		}
	case <-r.done:
		return false
	}
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

// DeliverTo enqueues a frame to one peer's connection (non-blocking, AD-12). It runs on
// the room goroutine — the sole owner of the conn table and the out channels — so it can
// never race the channel close on eviction/leave/terminate. Used for per-connection
// control frames that don't mutate room state, e.g. an {t:ice-refresh} re-mint.
func (r *Room) DeliverTo(id PeerID, f Frame) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, []outbound{{to: id, frame: f}})
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

// ObsStreaming reflects OBS's broadcast-level "we're live" state (D-24) to the room. Global,
// not slot-scoped, so it carries no epoch.
func (r *Room) ObsStreaming(active bool) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		deliver(conns, st.obsStreaming(active))
	})
}

// terminateBudget bounds how long Terminate waits PER PEER to enqueue a terminate frame
// into a backed-up out queue during a drain.
const terminateBudget = 2 * time.Second

// Terminate sends a terminate frame to every connected peer, then closes their out
// channels and clears the roster. Used for graceful shutdown (RF-21): the transient
// reason "reconnect" tells clients to retry with backoff (keyed by pass_id), so a
// deploy/restart isn't a hard mass-drop.
//
// terminate is a terminal control frame, so it must not be silently dropped on a full
// queue (RF-16). Each peer's send therefore BLOCKS until its writeLoop drains a slot,
// with its OWN budget so one wedged socket can't consume the time for the others; the
// peers are handled CONCURRENTLY so total time is ~one budget, not the sum. A genuinely
// stuck peer is given up on (its socket is dead anyway) and still closed. It runs on the
// room goroutine, so a concurrent readLoop Leave for a now-removed conn is a no-op
// (identity-checked).
func (r *Room) Terminate(reason string) {
	done := make(chan struct{})
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		st.terminating = true // refuse any late Join that arrives after this command
		var wg sync.WaitGroup
		for id, c := range conns {
			wg.Add(1)
			go func(c *peerConn) {
				defer wg.Done()
				t := time.NewTimer(terminateBudget)
				defer t.Stop()
				select {
				case c.out <- Frame{T: "terminate", Reason: reason}:
				case <-t.C: // this peer is wedged; give up on it
				}
				close(c.out)
			}(c)
			delete(conns, id)
		}
		wg.Wait()
		close(done)
	})
	select {
	case <-done:
	case <-r.done:
	}
}

// Close stops the room goroutine.
func (r *Room) Close() { close(r.done) }
