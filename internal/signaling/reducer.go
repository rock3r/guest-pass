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
	// terminating is set when the room is draining (Terminate). Once set, late joins are
	// refused so a connection can't slip into a room that has already sent its terminate
	// frames and is about to stop.
	terminating bool
	// streaming is the OBS broadcast-level "we're live" reflection (D-24), global to the
	// room (not slot-scoped): any source page may report it via streamingStarted/Stopped.
	streaming bool
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

// join registers a peer and emits the roster projection (EN-8): the joiner — if a
// greenroom participant — receives its rank-filtered roster, and every existing
// participant that can see the newcomer is told via peer-joined. OBS source pages are
// minimal (EN-13) and receive no roster; they are also host-only in others' projections.
func (s *roomState) join(id PeerID, role string) []outbound {
	// A reconnect (EN-16 eviction → re-join with the same id) is not a new arrival: the
	// peer never left room state and no peer-left ran, so re-announcing it would desync
	// other clients' rosters. The reconnecting connection still gets a fresh roster.
	_, rejoining := s.peers[id]
	s.peers[id] = &peerInfo{id: id, role: role}
	var out []outbound
	if isParticipant(role) {
		out = append(out, outbound{to: id, frame: Frame{T: "roster", Peers: s.rosterFor(role)}})
		// Replay the room's current OBS reflections (D-24) so a participant joining mid-stream
		// doesn't sit at the defaults until OBS next toggles: the global "we're live" state, and
		// the on-air of any slot this peer already occupies (an eviction-rejoin keeps its
		// binding). These events are otherwise transition-only.
		if s.streaming {
			out = append(out, outbound{to: id, frame: Frame{T: "streaming", Active: true}})
		}
		for sid, st := range s.slots {
			if st.occupant == id && st.onAir != OnAirUnknown {
				out = append(out, outbound{to: id, frame: Frame{T: "onair", Slot: string(sid), OnAir: st.onAir}})
			}
		}
	}
	if rejoining {
		return out
	}
	entry := RosterEntry{ID: string(id), Role: role}
	for pid, p := range s.peers {
		if pid == id || !isParticipant(p.role) {
			continue // only participants receive peer-joined; never echo to the joiner
		}
		if visibleTo(role, p.role) {
			out = append(out, outbound{to: pid, frame: Frame{T: "peer-joined", Peer: &entry}})
		}
	}
	return out
}

// leave removes a peer, detaches it from any slot it sourced or occupied, and tells the
// remaining participants it left (peer-left, projected). An occupied slot is unbound
// (epoch bump + placeholder) so a reconnecting source resolves to placeholder rather
// than the departed occupant (EN-3).
func (s *roomState) leave(id PeerID) []outbound {
	p := s.peers[id]
	delete(s.peers, id)
	var out []outbound
	for sid, st := range s.slots {
		if st.source == id {
			st.source = ""
			// The OBS reflection for this slot is gone — its on-air state is now UNKNOWN, not
			// whatever it last was (D-24: never assert on-air with no live signal behind it).
			// Degrade the occupant's pill. Streaming is room-global and not tied to any one
			// source, so it is NOT reset here.
			out = append(out, s.degradeOnAir(sid, st, st.occupant)...)
		}
		if st.occupant == id {
			out = append(out, s.unbindSlot(sid)...)
		}
	}
	if p != nil {
		for pid, rp := range s.peers {
			if !isParticipant(rp.role) {
				continue
			}
			if visibleTo(p.role, rp.role) {
				out = append(out, outbound{to: pid, frame: Frame{T: "peer-left", PeerID: string(id)}})
			}
		}
	}
	return out
}

// attachSource subscribes an OBS source page to a slot and immediately tells it the
// current binding so it can connect to the occupant (or show a placeholder). A
// (re)attaching source has reported no program transition yet, so the slot's on-air is
// UNKNOWN: on a reconnect/eviction (a source-page refresh re-opens /ws and Room.Join evicts
// the old conn WITHOUT running leave) the prior on-air may be stale — reset it and degrade
// the occupant's pill, so a refreshed source never strands a stale on-air (D-24).
func (s *roomState) attachSource(sid SlotID, source PeerID) []outbound {
	st := s.slot(sid)
	// A source replacing a still-registered one is an eviction reattach (the page reloaded and
	// Room.Join swapped the conn WITHOUT running leave), so the evicted socket may have an
	// in-flight sourceActive carrying the current epoch. Bump the epoch so those stale reports
	// are rejected by the epoch gate (EN-3) — the same mechanism that invalidates a prior
	// occupant's reports on rebind. A first attach (no prior source) has nothing to invalidate.
	if st.source != "" {
		st.epoch++
	}
	st.source = source
	out := s.degradeOnAir(sid, st, st.occupant)
	return append(out, outbound{to: source, frame: s.bindingFrame(sid, st)})
}

// degradeOnAir resets a slot's on-air to UNKNOWN and returns the frames degrading the given
// peers' self pills to status-unavailable — but ONLY if the slot was asserting a real state
// (on-air/not-on-air). A slot already UNKNOWN needs no notification, so a fresh bind stays
// quiet: this degrades a STALE assertion, it does not spam status-unavailable (D-24). Empty
// and duplicate peers are skipped, so the incoming and a displaced occupant can both be
// passed even when they are the same id.
func (s *roomState) degradeOnAir(sid SlotID, st *slotState, peers ...PeerID) []outbound {
	if st.onAir == OnAirUnknown {
		return nil
	}
	st.onAir = OnAirUnknown
	seen := map[PeerID]bool{}
	var out []outbound
	for _, p := range peers {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, outbound{to: p, frame: Frame{T: "onair", Slot: string(sid), OnAir: OnAirUnknown}})
	}
	return out
}

// epochPtr returns a pointer to a copy of e, so a slot frame carries its epoch (incl. 0,
// EN-3) while non-slot frames leave Epoch nil and omit it on the wire.
func epochPtr(e int) *int { return &e }

func (s *roomState) bindingFrame(sid SlotID, st *slotState) Frame {
	if st.occupant != "" {
		return Frame{T: "slot-rebind", Slot: string(sid), OccupantPeerID: string(st.occupant), Epoch: epochPtr(st.epoch)}
	}
	return Frame{T: "slot-unbound", Slot: string(sid), Epoch: epochPtr(st.epoch)}
}

// rebindSlot binds (or re-binds) a slot to an occupant: bump the epoch, reset on-air
// to unknown until a fresh obsSourceActiveChanged arrives (EN-3, so a stale
// active:true can't mislight the new occupant), and tell the source to renegotiate.
func (s *roomState) rebindSlot(sid SlotID, occupant PeerID) []outbound {
	if _, ok := s.peers[occupant]; !ok {
		return nil // ignore a rebind to an unknown/departed peer (don't advance epoch)
	}
	st := s.slot(sid)
	// If the slot was asserting a real on-air it is now stale (epoch bumps, the source
	// renegotiates): degrade BOTH the incoming occupant AND any displaced one — including the
	// case where they are the SAME id (a host re-applying the slot to bump the epoch) — so
	// neither keeps showing the prior pill (EN-3/D-24). A fresh bind stays quiet.
	out := s.degradeOnAir(sid, st, st.occupant, occupant)
	st.epoch++
	st.occupant = occupant
	st.onAir = OnAirUnknown
	if st.source != "" {
		out = append(out, outbound{to: st.source, frame: Frame{T: "slot-rebind", Slot: string(sid), OccupantPeerID: string(occupant), Epoch: epochPtr(st.epoch)}})
	}
	return out
}

// unbindSlot clears a slot (kick / leave): bump the epoch BEFORE any teardown
// broadcast (EN-3) and tell the source to fall back to a placeholder.
func (s *roomState) unbindSlot(sid SlotID) []outbound {
	st := s.slot(sid)
	// The unbound occupant is no longer sourced — degrade its pill if the slot was asserting a
	// real on-air (D-24). Harmless if it is also leaving the room (the frame to a since-removed
	// conn is dropped).
	out := s.degradeOnAir(sid, st, st.occupant)
	st.epoch++
	st.occupant = ""
	st.onAir = OnAirUnknown
	if st.source != "" {
		out = append(out, outbound{to: st.source, frame: Frame{T: "slot-unbound", Slot: string(sid), Epoch: epochPtr(st.epoch)}})
	}
	return out
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

// obsStreaming reflects OBS's broadcast-level "we're live" state (D-24) to every
// participant. Unlike per-slot on-air it is GLOBAL and not epoch-scoped — any source page
// may report it (obsStreamingStarted/Stopped). OBS source virtual peers don't receive it
// (they are minimal, EN-13); only greenroom participants (host/co-host/guest) do.
func (s *roomState) obsStreaming(active bool) []outbound {
	s.streaming = active
	var out []outbound
	for pid, p := range s.peers {
		if !isParticipant(p.role) {
			continue
		}
		out = append(out, outbound{to: pid, frame: Frame{T: "streaming", Active: active}})
	}
	return out
}

// relaySignal forwards a peer's SDP/ICE to the addressed peer, stamped with the sender.
// The sdp/ice payloads are opaque (json.RawMessage) and relayed byte-for-byte; the server
// never inspects them (D-23). It emits a CLEAN frame carrying only {t, from, sdp, ice} —
// never the sender's other fields — so a peer can't inject roster/slot/control fields into
// a frame the addressee acts on. A signal to an unknown/departed peer is dropped.
func (s *roomState) relaySignal(from PeerID, f Frame) []outbound {
	to := PeerID(f.To)
	if _, ok := s.peers[to]; !ok {
		return nil
	}
	return []outbound{{to: to, frame: Frame{T: "signal", From: string(from), SDP: f.SDP, ICE: f.ICE}}}
}
