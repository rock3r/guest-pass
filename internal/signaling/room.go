package signaling

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
func (r *Room) Join(id PeerID, role string, slot SlotID, out chan<- Frame) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		conns[id] = &peerConn{id: id, role: role, slot: slot, out: out}
		outs := st.join(id, role)
		if role == "obs" || role == "obs_screen" {
			outs = append(outs, st.attachSource(slot, id)...)
		}
		deliver(conns, outs)
	})
}

// Leave removes a peer and, crucially, CLOSES its out channel from the room
// goroutine — the only place that sends to it. Closing here (rather than in the
// transport) means a send can never race a close: all deliveries to this peer are
// already sequenced before this command on the single room goroutine.
func (r *Room) Leave(id PeerID) {
	r.post(func(st *roomState, conns map[PeerID]*peerConn) {
		outs := st.leave(id)
		c := conns[id]
		delete(conns, id)
		deliver(conns, outs)
		if c != nil {
			close(c.out)
		}
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

// Close stops the room goroutine.
func (r *Room) Close() { close(r.done) }
