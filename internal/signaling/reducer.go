package signaling

// PeerID and SlotID are stable identifiers within a room. A SlotID is the
// kind-qualified string from §4 (e.g. "cam-1", "host", "screen").
type PeerID string
type SlotID string

type peerInfo struct {
	id   PeerID
	role string
}

type slotState struct {
	occupant PeerID // currently bound occupant ("" = unbound)
	source   PeerID // the OBS source-page peer subscribed to this slot ("" = none)
	epoch    int    // monotonic; bumped on every (re)bind/unbind (EN-3)
	onAir    string // D-24 three-state; reset to OnAirUnknown on every rebind
}

// outbound is a frame the room wants delivered to a specific peer.
type outbound struct {
	to    PeerID
	frame Frame
}

// roomState is the PURE core of the room actor: every transition is
// (state mutation, []outbound) with no I/O, so it is exhaustively table-testable
// (AD-5/RF-25). The Room actor is its only caller and serializes all access, so the
// maps need no locks.
type roomState struct {
	peers map[PeerID]*peerInfo
	slots map[SlotID]*slotState
}

func newRoomState() *roomState {
	return &roomState{peers: map[PeerID]*peerInfo{}, slots: map[SlotID]*slotState{}}
}

func (s *roomState) slot(id SlotID) *slotState {
	st := s.slots[id]
	if st == nil {
		st = &slotState{onAir: OnAirUnknown}
		s.slots[id] = st
	}
	return st
}

func (s *roomState) join(id PeerID, role string) []outbound {
	s.peers[id] = &peerInfo{id: id, role: role}
	return nil // full roster projection (EN-8) is M3 work
}

// leave removes a peer and detaches it from any slot it sourced or occupied. An
// occupied slot is unbound (epoch bump + placeholder), so a reconnecting source
// resolves to placeholder rather than the departed occupant (EN-3).
func (s *roomState) leave(id PeerID) []outbound {
	delete(s.peers, id)
	var out []outbound
	for sid, st := range s.slots {
		if st.source == id {
			st.source = ""
		}
		if st.occupant == id {
			out = append(out, s.unbindSlot(sid)...)
		}
	}
	return out
}

// attachSource subscribes an OBS source page to a slot and immediately tells it the
// current binding so it can connect to the occupant (or show a placeholder).
func (s *roomState) attachSource(sid SlotID, source PeerID) []outbound {
	st := s.slot(sid)
	st.source = source
	return []outbound{{to: source, frame: s.bindingFrame(sid, st)}}
}

func (s *roomState) bindingFrame(sid SlotID, st *slotState) Frame {
	if st.occupant != "" {
		return Frame{T: "slot-rebind", Slot: string(sid), OccupantPeerID: string(st.occupant), Epoch: st.epoch}
	}
	return Frame{T: "slot-unbound", Slot: string(sid), Epoch: st.epoch}
}

// rebindSlot binds (or re-binds) a slot to an occupant: bump the epoch, reset on-air
// to unknown until a fresh obsSourceActiveChanged arrives (EN-3, so a stale
// active:true can't mislight the new occupant), and tell the source to renegotiate.
func (s *roomState) rebindSlot(sid SlotID, occupant PeerID) []outbound {
	if _, ok := s.peers[occupant]; !ok {
		return nil // ignore a rebind to an unknown/departed peer (don't advance epoch)
	}
	st := s.slot(sid)
	st.epoch++
	st.occupant = occupant
	st.onAir = OnAirUnknown
	if st.source == "" {
		return nil
	}
	return []outbound{{to: st.source, frame: Frame{T: "slot-rebind", Slot: string(sid), OccupantPeerID: string(occupant), Epoch: st.epoch}}}
}

// unbindSlot clears a slot (kick / leave): bump the epoch BEFORE any teardown
// broadcast (EN-3) and tell the source to fall back to a placeholder.
func (s *roomState) unbindSlot(sid SlotID) []outbound {
	st := s.slot(sid)
	st.epoch++
	st.occupant = ""
	st.onAir = OnAirUnknown
	if st.source == "" {
		return nil
	}
	return []outbound{{to: st.source, frame: Frame{T: "slot-unbound", Slot: string(sid), Epoch: st.epoch}}}
}

// obsSourceActive applies an OBS on-program reflection ONLY when its epoch matches
// the slot's current epoch (EN-3): a stale event from a previous occupant is ignored
// so it cannot mislight the new occupant; a future epoch is also ignored.
func (s *roomState) obsSourceActive(sid SlotID, active bool, epoch int) []outbound {
	st := s.slots[sid]
	if st == nil || epoch != st.epoch {
		return nil
	}
	if active {
		st.onAir = OnAirYes
	} else {
		st.onAir = OnAirNo
	}
	if st.occupant == "" {
		return nil
	}
	return []outbound{{to: st.occupant, frame: Frame{T: "onair", Slot: string(sid), OnAir: st.onAir}}}
}

// relaySignal forwards an SDP/ICE frame verbatim to its addressed peer; the server
// never inspects the payload (D-23). Frames to unknown peers are dropped.
func (s *roomState) relaySignal(from PeerID, f Frame) []outbound {
	to := PeerID(f.To)
	if _, ok := s.peers[to]; !ok {
		return nil
	}
	f.From = string(from)
	f.To = ""
	return []outbound{{to: to, frame: f}}
}
