package signaling

// PeerID and SlotID are stable identifiers within a room. A SlotID is the
// kind-qualified string from §4 (e.g. "cam-1", "host", "screen").
type PeerID string
type SlotID string

type peerInfo struct {
	id   PeerID
	role string
	name string // display name resolved from auth at handshake (EN-7), never from a frame
	// Self-presence (EN-7), set by {t:state}. cam/mic/screen are the live modality flags
	// folded into the roster; handRaised is the soft "bring me in" nudge (PR-7).
	cam, mic, screen bool
	handRaised       bool
	// level is the last reported audio meter (0..1), held in-memory only (EN-11: never
	// persisted) and coalesced onto the batched {t:levels} tick (AD-13), never the roster.
	level float64
	// Degradation self-report (AD-21), set by {t:stats}: signal is a coarse 1..5 connection-health
	// level (0 = unknown), rttMs the round-trip estimate, and degraded the active shedding state
	// (nil = not degraded). In-memory only (EN-11: per-frame stats are never persisted); folded
	// into the roster so the host sees per-tile health + a degrading/recovering badge (PR-13/14).
	signal   int
	rttMs    int
	degraded *DegradedView
}

// lockState is a suppression lock on one (target, modality): the applier and the rank FLOOR
// at which it was set (D-13). A higher-rank force raises the floor + owner; a lower-or-equal
// force is a no-op; release needs the actor's CURRENT rank ≥ floor (demotion-safe, EN-7).
type lockState struct {
	applier PeerID
	floor   int // rank floor: rankCohost or rankHost (a guest can never force)
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
	// levelsActive tracks whether the previous {t:levels} tick carried sound, so the tick
	// stays silent in an idle/quiet room (no spam) yet still emits ONE trailing all-zero
	// frame when the room goes quiet, letting clients settle their meters to 0 (AD-13).
	levelsActive bool
	// locks are the active suppression locks, keyed target → modality (mic|cam|share) → lock
	// (D-13/EN-7). They live at ROOM scope (not on peerInfo) so they survive a target's full
	// disconnect and re-apply on reconnect; they are seeded from the pass_locks table on room
	// (re)spawn and persisted on force/release (AD-22), so a force-muted guest stays muted
	// across a restart.
	locks map[PeerID]map[string]*lockState
}

func newRoomState() *roomState {
	return &roomState{
		peers: map[PeerID]*peerInfo{},
		slots: map[SlotID]*slotState{},
		locks: map[PeerID]map[string]*lockState{},
	}
}

// lockOn returns the suppression lock on (target, modality), or nil.
func (s *roomState) lockOn(target PeerID, modality string) *lockState {
	return s.locks[target][modality]
}

// locked reports whether (target, modality) is suppression-locked.
func (s *roomState) locked(target PeerID, modality string) bool {
	return s.lockOn(target, modality) != nil
}

// setLock installs/overwrites the lock on (target, modality).
func (s *roomState) setLock(target PeerID, modality string, lk *lockState) {
	if s.locks[target] == nil {
		s.locks[target] = map[string]*lockState{}
	}
	s.locks[target][modality] = lk
}

// clearLock removes the lock on (target, modality), tidying the empty inner map.
func (s *roomState) clearLock(target PeerID, modality string) {
	delete(s.locks[target], modality)
	if len(s.locks[target]) == 0 {
		delete(s.locks, target)
	}
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
func (s *roomState) join(id PeerID, role, name string) []outbound {
	// A reconnect (EN-16 eviction → re-join with the same id) is not a new arrival: the
	// peer never left room state and no peer-left ran, so re-announcing it would desync
	// other clients' rosters. The reconnecting connection still gets a fresh roster. Preserve
	// any presence/hand state a still-registered peer already had (a rejoin keeps its slot
	// binding, so it keeps its folded on-air too — carried in the fresh roster, not replayed).
	prev, rejoining := s.peers[id]
	p := &peerInfo{id: id, role: role, name: name}
	if prev != nil {
		p.cam, p.mic, p.screen, p.handRaised = prev.cam, prev.mic, prev.screen, prev.handRaised
	}
	// Suppression locks live at ROOM scope (s.locks), independent of this peerInfo, so a
	// reconnect — or a full disconnect + rejoin, or a respawn from the pass_locks table — keeps
	// the target locked: a force-muted target can never self-release by reconnecting (D-13/EN-7).
	s.peers[id] = p
	var out []outbound
	if isParticipant(role) {
		// The roster carries the joiner's full projection including its own (self-marked) entry,
		// whose onAir field folds in any slot it already occupies (D-24) — so a mid-stream joiner
		// gets its on-air pill straight from the roster, no separate replay needed.
		out = append(out, s.rosterFrame(id, role))
		// The broadcast-level "we're live" state is room-global (not slot-scoped, not in the
		// roster), so it is still replayed to a participant joining mid-stream (D-24).
		if s.streaming {
			out = append(out, outbound{to: id, frame: Frame{T: "streaming", Active: true}})
		}
	}
	if rejoining {
		return out
	}
	entry := s.entryFor(p)
	for pid, rp := range s.peers {
		if pid == id || !isParticipant(rp.role) {
			continue // only participants receive peer-joined; never echo to the joiner
		}
		if visibleTo(role, rp.role) {
			out = append(out, outbound{to: pid, frame: Frame{T: "peer-joined", Peer: &entry}})
		}
	}
	return out
}

// rosterFrame builds the {t:roster} frame for one recipient: its rank-filtered projection
// (EN-8) plus the recipient's own peer id, so a client can locate its self entry (e.g. the
// guest self on-air pill, which the entry's onAir field now carries instead of {t:onair}).
func (s *roomState) rosterFrame(id PeerID, role string) outbound {
	return outbound{to: id, frame: Frame{T: "roster", Recipient: string(id), Peers: s.rosterFor(id, role)}}
}

// rebroadcastRoster re-sends every participant its current projected roster. It is the
// vehicle for a roster-visible change that is not a join/leave delta — a presence toggle
// ({t:state}), a folded on-air change, a hand-raise, and (later) a lock or role change.
// Full roster frames go out only on these structural changes; continuous audio meters ride
// the separate batched {t:levels} tick (AD-13), so this is not an N² hot path.
func (s *roomState) rebroadcastRoster() []outbound {
	var out []outbound
	for pid, p := range s.peers {
		if isParticipant(p.role) {
			out = append(out, s.rosterFrame(pid, p.role))
		}
	}
	return out
}

// applyState folds a participant's self-presence ({t:state}, EN-7) into the roster: each
// PROVIDED (non-nil) modality updates, and a presence change re-broadcasts the roster so every
// viewer's tile reflects it. An absent modality is left unchanged, so a meter-only update
// ({t:state,level}) does NOT clobber presence to off — and a presence-less update emits
// nothing, avoiding roster churn. The audio meter is stored in-memory only (EN-11) and rides
// the batched {t:levels} tick (AD-13), NEVER the roster, so a level change alone never
// re-broadcasts. A non-participant (OBS source) has no presence and is ignored. Lock
// enforcement against a suppressed modality lands in PR-3.
func (s *roomState) applyState(id PeerID, cam, mic, screen *bool, level *float64) []outbound {
	p := s.peers[id]
	if p == nil || !isParticipant(p.role) {
		return nil
	}
	if level != nil {
		p.level = *level // in-memory only; coalesced onto the {t:levels} tick, not the roster
	}
	changed, violated := false, false
	// apply folds one provided modality into presence, but REJECTS a self-state that tries to
	// re-enable a suppression-locked modality (EN-7): the server is the enforcement point since
	// UI gating is bypassable. A rejected re-enable is a no-op but flips `violated` so the
	// target's optimistic UI is corrected by an authoritative re-broadcast.
	apply := func(pres *bool, want *bool, modality string) {
		if want == nil {
			return
		}
		if *want && s.locked(id, modality) {
			violated = true // can't self-enable a force-suppressed modality
			return
		}
		if *pres != *want {
			*pres, changed = *want, true
		}
	}
	apply(&p.cam, cam, "cam")
	apply(&p.mic, mic, "mic")
	apply(&p.screen, screen, "share")
	if changed {
		return s.rebroadcastRoster()
	}
	if violated {
		// Nothing legitimately changed, but the target tried to defy a lock: re-send ONLY the
		// violating target its authoritative roster so its UI snaps back (no room-wide churn).
		return []outbound{s.rosterFrame(id, p.role)}
	}
	return nil
}

// applyStats folds a publisher's {t:stats} self-report (AD-21) into its roster entry: signal +
// degraded drive the host's per-tile connection health and degrading/recovering badge. Only a
// participant reports stats (an OBS source reflects on-air, not media health). rttMs ticks on every
// sample, so a report that leaves signal AND degraded unchanged is a NO-OP that must not spam the
// roster (EN-11: per-frame stats live in memory and are never persisted). A material change — the
// signal level, or the degraded direction/reason — folds in and re-broadcasts.
func (s *roomState) applyStats(id PeerID, signal, rttMs int, degraded *DegradedView) []outbound {
	p := s.peers[id]
	if p == nil || !isParticipant(p.role) {
		return nil
	}
	changed := p.signal != signal || !sameDegraded(p.degraded, degraded)
	p.signal, p.rttMs, p.degraded = signal, rttMs, degraded
	if changed {
		return s.rebroadcastRoster()
	}
	return nil
}

// sameDegraded compares two degradation views by value (nil == nil; the pointers are never aliased).
func sameDegraded(a, b *DegradedView) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// buildLevels coalesces every participant's last-reported audio meter into ONE batched
// {t:levels} frame per participant (AD-13) — never N² roster spam at the cap, never the
// roster, never persisted (EN-11). It is silent in a quiet room: it emits while any
// participant has sound, plus ONE trailing all-zero frame when the room falls silent (so
// clients settle their meters), then nothing until sound returns. OBS source virtual peers
// have no meter and neither send nor receive it (EN-13).
func (s *roomState) buildLevels() []outbound {
	levels := map[string]float64{}
	anyActive := false
	for id, p := range s.peers {
		if !isParticipant(p.role) {
			continue
		}
		levels[string(id)] = p.level
		if p.level > 0 {
			anyActive = true
		}
	}
	if !anyActive && !s.levelsActive {
		return nil // quiet room, already settled — no idle spam
	}
	s.levelsActive = anyActive
	var out []outbound
	for id, p := range s.peers {
		if isParticipant(p.role) {
			out = append(out, outbound{to: id, frame: Frame{T: "levels", Levels: levels}})
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
			// D-38: tell the bound occupant (the guest whose publisher served this source) that this
			// consumer is gone, so its connectivity watchdog untracks the never-connected source pc and
			// can't trip a false "your network blocks peer-to-peer". A guest receives no peer-left for a
			// source (sources aren't participants — they're hidden by visibleTo, roster.go), so this
			// {t:consumer-left} is the source's analogue, sent ONLY to the occupant. The peer id is one
			// the guest already answered on the source's offer; the source token is never in the frame.
			if st.occupant != "" {
				out = append(out, outbound{to: st.occupant, frame: Frame{T: "consumer-left", PeerID: string(id)}})
			}
			st.source = ""
			// The OBS reflection for this slot is gone — its on-air is now UNKNOWN, not whatever
			// it last was (D-24: never assert on-air with no live signal behind it). Reset it; the
			// occupant's folded onAir then degrades in the roster re-broadcast below. Streaming is
			// room-global and not tied to any one source, so it is NOT reset here.
			s.degradeStaleOnAir(st)
		}
		if st.occupant == id {
			// The occupant left: free its slot (epoch bump + placeholder) so a reconnecting source
			// resolves to placeholder, not the departed occupant (EN-3). Only the source frame is
			// collected here; the roster re-broadcast below folds in the now-vacated slot.
			out = append(out, s.vacateSlot(sid)...)
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
	// The leaver is already removed, so the re-broadcast excludes it; remaining viewers see any
	// freed slot / degraded on-air folded into their roster (alongside the peer-left delta).
	return append(out, s.rebroadcastRoster()...)
}

// attachSource subscribes an OBS source page to a slot and immediately tells it the current
// binding so it can connect to the occupant (or show a placeholder). A (re)attaching source
// has reported no program transition yet, so the slot's on-air is UNKNOWN: on a
// reconnect/eviction (a source-page refresh re-opens /ws and Room.Join evicts the old conn
// WITHOUT running leave) the prior on-air may be stale — reset it so a refreshed source never
// strands a stale on-air (D-24). The occupant's folded onAir then degrades in the roster.
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
	staleReset := s.degradeStaleOnAir(st)
	out := []outbound{{to: source, frame: s.bindingFrame(sid, st)}}
	// A source (re)attaching to an OCCUPIED slot also gets the occupant's current lock kinds (RF-8),
	// so a pre-existing lock (force-then-source-connects, or a seeded lock re-applied on reconnect
	// after a restart) detaches the locked remote track the moment the source binds. An unbound slot
	// has no occupant, so no occupant-locks.
	if st.occupant != "" {
		out = append(out, outbound{to: source, frame: s.occupantLocksFrame(sid, st)})
	}
	// Only a stale-on-air reset (an occupied slot that WAS asserting a real state) changes a
	// roster entry; a fresh attach to a quiet slot stays silent — no roster churn.
	if staleReset && st.occupant != "" {
		out = append(out, s.rebroadcastRoster()...)
	}
	return out
}

// degradeStaleOnAir resets a slot's on-air to UNKNOWN if it was asserting a real state, so a
// stale assertion never outlives the live OBS signal behind it (D-24). Returns whether it
// changed; the caller re-broadcasts the roster so the occupant's folded onAir field updates.
func (s *roomState) degradeStaleOnAir(st *slotState) bool {
	if st.onAir == OnAirUnknown {
		return false
	}
	st.onAir = OnAirUnknown
	return true
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

// occupantLocksFrame projects a bound occupant's active lock KINDS to that slot's OBS source page
// (RF-8 receiver-side enforcement): an OBS source gets no roster (EN-13), so it learns the lock here
// and detaches the locked REMOTE track from the program output, independent of the (possibly
// modified) occupant. It rides the slot epoch + occupantPeerId so the source can ignore a straggler
// for a previous occupant/epoch (EN-3), mirroring the slot-rebind gate; it is KINDS-ONLY (no applier
// identity) to keep the crown-jewel source page minimal (EN-5).
func (s *roomState) occupantLocksFrame(sid SlotID, st *slotState) Frame {
	return Frame{T: "occupant-locks", Slot: string(sid), OccupantPeerID: string(st.occupant), Epoch: epochPtr(st.epoch), LockKinds: s.lockKindsOf(st.occupant)}
}

// sourceLockFrames emits an occupant-locks frame to the OBS source page of every slot whose occupant
// is target, so a lock/release on target reaches the air-critical source surface (RF-8) even though
// sources receive no roster. Returns nil when target occupies no sourced slot.
func (s *roomState) sourceLockFrames(target PeerID) []outbound {
	var out []outbound
	for sid, st := range s.slots {
		if st.occupant != target || st.source == "" {
			continue
		}
		out = append(out, outbound{to: st.source, frame: s.occupantLocksFrame(sid, st)})
	}
	return out
}

// bindSlot is the core of a (re)bind: degrade any stale on-air, bump the epoch, set the new
// occupant, and reset on-air to UNKNOWN (EN-3, so a stale active:true can't mislight the new
// occupant). It returns ONLY the slot-rebind frame to the source; the caller re-broadcasts
// the roster (occupancy changed, so both the old and new occupant's folded onAir move).
func (s *roomState) bindSlot(sid SlotID, occupant PeerID) []outbound {
	st := s.slot(sid)
	prev := st.occupant // before reassignment, for the consumer-left notice below
	s.degradeStaleOnAir(st)
	st.epoch++
	st.occupant = occupant
	st.onAir = OnAirUnknown
	if st.source == "" {
		return nil
	}
	var out []outbound
	// D-38: the source is moving to a new occupant, so the PRIOR occupant's publisher is left with a
	// (possibly never-connected) pc to this source that it gets no peer-left for (sources aren't in
	// guest rosters, EN-13). Tell the prior occupant the consumer is gone so its watchdog untracks it
	// and can't trip a false network-blocked. Skip a no-op re-bind (same occupant, e.g. a reconnect)
	// and a prior occupant that already left the room (its publisher is closing; the leave path owns it).
	if prev != "" && prev != occupant && s.peers[prev] != nil {
		out = append(out, outbound{to: prev, frame: Frame{T: "consumer-left", PeerID: string(st.source)}})
	}
	// Send the binding, then the new occupant's authoritative lock kinds (RF-8): a (re)bind to a
	// locked occupant detaches the locked track at once, and a rebind to a fresh occupant clears any
	// stale lock view the source held for the previous occupant (empty kinds → re-enable all).
	return append(out,
		outbound{to: st.source, frame: Frame{T: "slot-rebind", Slot: string(sid), OccupantPeerID: string(occupant), Epoch: epochPtr(st.epoch)}},
		outbound{to: st.source, frame: s.occupantLocksFrame(sid, st)},
	)
}

// rebindSlot binds (or re-binds) a slot to an occupant and re-broadcasts the roster so the
// occupants' folded on-air pills move (EN-3/D-24). A rebind naming a peer not in the room is
// a no-op (no epoch advance) — the slot must never bind to a peer that can't receive media.
func (s *roomState) rebindSlot(sid SlotID, occupant PeerID) []outbound {
	if _, ok := s.peers[occupant]; !ok {
		return nil
	}
	out := s.bindSlot(sid, occupant)
	return append(out, s.rebroadcastRoster()...)
}

// vacateSlot is the core of clearing a slot (kick / leave / unbind): degrade stale on-air,
// bump the epoch BEFORE any teardown broadcast (EN-3), clear the occupant, and reset on-air.
// It returns ONLY the slot-unbound frame to the source; the caller folds the vacated slot
// into the roster.
func (s *roomState) vacateSlot(sid SlotID) []outbound {
	st := s.slot(sid)
	prev := st.occupant // before clearing, for the consumer-left notice below
	s.degradeStaleOnAir(st)
	st.epoch++
	st.occupant = ""
	st.onAir = OnAirUnknown
	if st.source == "" {
		return nil
	}
	var out []outbound
	// D-38: the source loses its occupant, leaving that occupant's publisher with a stale source pc.
	// Tell it the consumer is gone (same as bindSlot) — UNLESS it already left the room (the leave
	// path vacates after removing the peer, so s.peers[prev] is nil and we skip the pointless notice).
	if prev != "" && s.peers[prev] != nil {
		out = append(out, outbound{to: prev, frame: Frame{T: "consumer-left", PeerID: string(st.source)}})
	}
	return append(out, outbound{to: st.source, frame: Frame{T: "slot-unbound", Slot: string(sid), Epoch: epochPtr(st.epoch)}})
}

// unbindSlot clears a slot and re-broadcasts the roster so the freed occupant's folded on-air
// degrades to status-unavailable (D-24). Used by the host {t:unbind} and by kick (PR-8).
func (s *roomState) unbindSlot(sid SlotID) []outbound {
	out := s.vacateSlot(sid)
	return append(out, s.rebroadcastRoster()...)
}

// obsSourceActive folds an OBS on-program reflection into the occupant's roster onAir, but
// ONLY when its epoch matches the slot's current epoch (EN-3): a stale event from a previous
// occupant is ignored so it can't mislight the new occupant; a future epoch is also ignored.
// A real change with a bound occupant re-broadcasts the roster; an unchanged or unoccupied
// slot stays silent.
func (s *roomState) obsSourceActive(sid SlotID, active bool, epoch int) []outbound {
	st := s.slots[sid]
	if st == nil || epoch != st.epoch {
		return nil
	}
	want := OnAirNo
	if active {
		want = OnAirYes
	}
	changed := st.onAir != want
	st.onAir = want
	if !changed || st.occupant == "" {
		return nil
	}
	return s.rebroadcastRoster()
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

// recoverQuality broadcasts a host "bump quality now" to every participant (AD-21/D-34): each
// publisher restores its own shed senders immediately, overriding the slow recover hysteresis. It
// carries no state and touches no media — the actual recovery is per-publisher-local (D-23).
func (s *roomState) recoverQuality() []outbound {
	var out []outbound
	for pid, p := range s.peers {
		if !isParticipant(p.role) {
			continue
		}
		out = append(out, outbound{to: pid, frame: Frame{T: "recover-quality"}})
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
	// A source page's signals are valid only for its slot's CURRENT occupant. After an unbind/rebind a
	// source can still emit a stale offer/ICE toward the PRIOR occupant before it processes the slot
	// change; relaying that would let the prior occupant recreate a dead source pc — re-arming the D-38
	// watchdog even after its {t:consumer-left}. Drop a source→non-occupant signal (a source only ever
	// negotiates its bound occupant; the occupant→source direction is unaffected).
	if p := s.peers[from]; p != nil && (p.role == "obs" || p.role == "obs_screen") && !s.sourceServes(from, to) {
		return nil
	}
	return []outbound{{to: to, frame: Frame{T: "signal", From: string(from), SDP: f.SDP, ICE: f.ICE}}}
}

// sourceServes reports whether source is the OBS source of a slot whose CURRENT occupant is occupant
// (the only peer a source legitimately negotiates with). A source attached to no slot serves no one.
func (s *roomState) sourceServes(source, occupant PeerID) bool {
	for _, st := range s.slots {
		if st.source == source {
			return st.occupant == occupant
		}
	}
	return false
}

// relayChat broadcasts a backstage chat message to every greenroom PARTICIPANT, including the
// sender (a server-authoritative thread), stamped with the sender's id from auth (EN-7 — the
// client's own `from` is ignored). OBS source virtual peers are minimal and never receive chat
// (EN-13). This is a PURE relay: it builds outbound frames and returns them — it touches no
// store and no log, so the EN-20 guarantee (backstage chat is never persisted, `text` is never
// logged) holds by construction. Only the sender (a participant) may chat; an empty sender or a
// non-participant is dropped.
func (s *roomState) relayChat(from PeerID, text string) []outbound {
	if p := s.peers[from]; p == nil || !isParticipant(p.role) {
		return nil
	}
	var out []outbound
	for pid, p := range s.peers {
		if isParticipant(p.role) {
			out = append(out, outbound{to: pid, frame: Frame{T: "chat", From: string(from), Text: text}})
		}
	}
	return out
}
