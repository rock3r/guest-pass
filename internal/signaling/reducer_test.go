package signaling

import (
	"encoding/json"
	"strings"
	"testing"
)

// rosterEntryFor returns peer's entry as projected in the first {t:roster} frame addressed
// to `to`, or false if there is no such roster or entry. On-air, presence and locks fold
// into the roster (D-24/EN-8), so this is how a behavior is asserted post-M3.
func rosterEntryFor(out []outbound, to, peer PeerID) (RosterEntry, bool) {
	f, ok := firstFrameOfType(out, to, "roster")
	if !ok {
		return RosterEntry{}, false
	}
	for _, e := range f.Peers {
		if e.ID == string(peer) {
			return e, true
		}
	}
	return RosterEntry{}, false
}

// onAirSeenBy returns the folded three-state on-air of `peer` in the roster delivered to
// `to` (D-24), or "" if no such roster/entry was emitted.
func onAirSeenBy(out []outbound, to, peer PeerID) string {
	e, _ := rosterEntryFor(out, to, peer)
	return e.OnAir
}

// EN-3: a rebind bumps the epoch, swaps the occupant, resets on-air to unknown, and tells
// the source page to renegotiate to the new occupant at the new epoch. The occupant's folded
// on-air starts at status-unavailable until a fresh transition (D-24).
func TestRebindBumpsEpochAndResetsOnAir(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")

	out := s.rebindSlot("cam-1", "g1")

	st := s.slots["cam-1"]
	if st.epoch != 1 || st.occupant != "g1" || st.onAir != OnAirUnknown {
		t.Fatalf("slot state = %+v, want epoch 1 / g1 / %s", st, OnAirUnknown)
	}
	if sr, ok := firstFrameOfType(out, "src", "slot-rebind"); !ok || sr.OccupantPeerID != "g1" || epochVal(sr) != 1 {
		t.Fatalf("source should get slot-rebind(g1, epoch 1), got %+v", out)
	}
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("g1's folded on-air after rebind = %q, want %s", got, OnAirUnknown)
	}
}

// AC-1/T-1: a participant's {t:state} folds its cam/mic/screen into the roster — every
// viewer's tile reflects it, and the sender's own (self-marked) entry carries it too.
func TestStateFoldsPresenceIntoRoster(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "Greta")

	out := s.applyState("g1", false, true, false) // mic on, cam + screen off

	e, ok := rosterEntryFor(out, "host", "g1")
	if !ok {
		t.Fatalf("host roster should include g1, got %+v", out)
	}
	if e.Cam || !e.Mic || e.Screen || e.Name != "Greta" {
		t.Fatalf("g1 presence = %+v, want name=Greta cam:false mic:true screen:false", e)
	}
	self, ok := rosterEntryFor(out, "g1", "g1")
	if !ok || !self.Self || !self.Mic {
		t.Fatalf("g1's own entry should be self-marked with mic on, got %+v", self)
	}
}

// AD-13: a {t:state} that changes nothing (e.g. a level-only tick) must NOT re-broadcast
// the roster — continuous meters ride the {t:levels} tick (PR-2), not the roster.
func TestStateNoChangeIsQuiet(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyState("g1", true, true, false)
	if out := s.applyState("g1", true, true, false); out != nil {
		t.Fatalf("an unchanged state must not churn the roster, got %+v", out)
	}
}

// EN-7: an OBS source virtual peer has no self-presence; a {t:state} from it is ignored.
func TestStateFromNonParticipantIgnored(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	if out := s.applyState("src", true, true, true); out != nil {
		t.Fatalf("an OBS source has no presence; state must be ignored, got %+v", out)
	}
}

// D-24: a source's streamingStarted/Stopped reflection broadcasts a GLOBAL "we're live"
// state to every participant (host/co-host/guest) — but never to OBS source virtual peers
// (EN-13) — and is not epoch-scoped. This stays a room-level broadcast (not folded into the
// per-guest roster, which carries the per-slot onAir).
func TestObsStreamingBroadcastsToParticipantsOnly(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("src-cam-1", "obs", "") // a source page — must NOT receive the streaming broadcast

	out := s.obsStreaming(true)
	if !s.streaming {
		t.Fatalf("obsStreaming(true) must set the room streaming state")
	}
	got := map[PeerID]bool{}
	for _, o := range out {
		if o.frame.T != "streaming" || !o.frame.Active {
			t.Fatalf("unexpected outbound %+v, want {t:streaming, active:true}", o.frame)
		}
		got[o.to] = true
	}
	if !got["host"] || !got["g1"] {
		t.Fatalf("host and g1 must receive the streaming broadcast, got %v", got)
	}
	if got["src-cam-1"] {
		t.Fatalf("OBS source pages must NOT receive the streaming broadcast (EN-13), got %v", got)
	}

	// streamingStopped flips the global state back and carries active=false.
	out = s.obsStreaming(false)
	if s.streaming {
		t.Fatalf("obsStreaming(false) must clear the room streaming state")
	}
	for _, o := range out {
		if o.frame.Active {
			t.Fatalf("a streamingStopped broadcast must carry active=false, got %+v", o.frame)
		}
	}
}

// D-24: when the OBS source for a slot disconnects, its on-air reflection is gone — the
// occupant's folded on-air must degrade to status-unavailable, never keep asserting a stale
// on-air with no live OBS signal behind it ("never assert when unknown").
func TestSourceLeaveResetsOnAirToUnavailable(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")         // epoch 1, occupant g1
	s.obsSourceActive("cam-1", true, 1) // slot reflects on-air
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("precondition: slot should be on-air, got %q", s.slots["cam-1"].onAir)
	}

	out := s.leave("src") // the OBS source disconnects

	if st := s.slots["cam-1"]; st.onAir != OnAirUnknown {
		t.Fatalf("on-air must degrade to %s when the source leaves, got %q", OnAirUnknown, st.onAir)
	}
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("occupant g1's roster must show on-air degraded to %s, got %q (out=%+v)", OnAirUnknown, got, out)
	}
}

// EN-3 (the keystone): after a rebind, a STALE obsSourceActive carrying the previous epoch
// must NOT light the new occupant; only the current epoch's event applies.
func TestStaleObsActiveIgnoredAfterRebind(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1") // epoch 1

	out := s.obsSourceActive("cam-1", true, 1)
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("epoch-1 active should light the slot, got %q", s.slots["cam-1"].onAir)
	}
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirYes {
		t.Fatalf("g1's roster should show on-air, got %q", got)
	}

	s.join("g2", "guest", "")
	s.rebindSlot("cam-1", "g2") // epoch 2, on-air reset
	if s.slots["cam-1"].onAir != OnAirUnknown {
		t.Fatalf("rebind must reset on-air to %s", OnAirUnknown)
	}

	// stale epoch-1 active from the old occupant: ignored, emits nothing.
	if out := s.obsSourceActive("cam-1", true, 1); out != nil {
		t.Fatalf("stale epoch-1 active must be ignored, got %+v", out)
	}
	if s.slots["cam-1"].onAir != OnAirUnknown {
		t.Fatalf("stale active must NOT light the new occupant (EN-3)")
	}

	// current epoch lights g2.
	s.obsSourceActive("cam-1", true, 2)
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("epoch-2 active should light g2")
	}
}

// A source that attaches after a slot is already bound learns the current binding.
func TestAttachSourceGetsCurrentBinding(t *testing.T) {
	s := newRoomState()
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1") // bound before any source attaches

	s.join("src", "obs", "")
	out := s.attachSource("cam-1", "src")
	if sr, ok := firstFrameOfType(out, "src", "slot-rebind"); !ok || sr.OccupantPeerID != "g1" {
		t.Fatalf("attach should deliver the current binding to the source, got %+v", out)
	}
}

// A rebind naming a peer that isn't in the room is a no-op: it must not advance the
// epoch or bind the slot to a peer that can't receive media/on-air.
func TestRebindToUnknownPeerIsNoOp(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")

	if out := s.rebindSlot("cam-1", "ghost"); out != nil {
		t.Fatalf("rebind to unknown peer should emit nothing, got %+v", out)
	}
	if st := s.slots["cam-1"]; st.epoch != 0 || st.occupant != "" {
		t.Fatalf("rebind to unknown peer must not mutate the slot, got %+v", st)
	}
}

func TestUnbindBumpsEpochAndPlaceholders(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")

	out := s.unbindSlot("cam-1")
	st := s.slots["cam-1"]
	if st.epoch != 2 || st.occupant != "" || st.onAir != OnAirUnknown {
		t.Fatalf("unbind state = %+v", st)
	}
	if su, ok := firstFrameOfType(out, "src", "slot-unbound"); !ok || epochVal(su) != 2 {
		t.Fatalf("source should get slot-unbound(epoch 2), got %+v", out)
	}
}

// D-24: unbinding a slot that WAS on-air degrades the occupant's folded on-air to
// status-unavailable (it is no longer sourced), alongside the slot-unbound to the source.
func TestUnbindDegradesOnAirOccupant(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, 1) // g1 on-air

	out := s.unbindSlot("cam-1")
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("unbinding an on-air slot must degrade the occupant's pill (D-24), got %q (out=%+v)", got, out)
	}
}

// D-24: reassigning a slot from occupant A to B must degrade A's pill — A is no longer
// sourced here, so it can't keep asserting the on-air it last had (later sourceActive frames
// go to B). The new occupant B's pill stays at its default until a fresh transition.
func TestRebindDegradesDisplacedOccupant(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("a", "guest", "")
	s.join("b", "guest", "")
	s.rebindSlot("cam-1", "a")
	s.obsSourceActive("cam-1", true, 1) // a is on-air

	out := s.rebindSlot("cam-1", "b") // reassign the slot to b

	if got := onAirSeenBy(out, "a", "a"); got != OnAirUnknown {
		t.Fatalf("displaced occupant a must degrade to %s, got %q", OnAirUnknown, got)
	}
	// The incoming occupant b must NEVER inherit the prior occupant's on-air (EN-3).
	if got := onAirSeenBy(out, "b", "b"); got != OnAirUnknown {
		t.Fatalf("the new occupant must not inherit a stale on-air, got %q", got)
	}
}

// D-24: re-applying a slot to the SAME occupant (a host bumping the epoch to renegotiate)
// still degrades that occupant — the slot's on-air is stale at the new epoch until a fresh
// transition, so the guest must not keep showing the prior pill.
func TestRebindSameOccupantStillDegrades(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, 1) // g1 on-air

	out := s.rebindSlot("cam-1", "g1") // re-apply to the same occupant

	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("re-applying a slot to the same on-air occupant must degrade its pill (D-24), got %q", got)
	}
}

// D-24: a source reconnect/reload that re-attaches (Room.Join evicts the old conn WITHOUT
// running leave) must reset a stale on-air and degrade the occupant — the refreshed source
// has reported no transition yet, so the state is UNKNOWN.
func TestSourceReattachResetsStaleOnAir(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, 1) // slot is on-air
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("precondition: slot should be on-air")
	}

	// The source page refreshes: the replacement connection re-attaches to the same slot.
	out := s.attachSource("cam-1", "src")

	if st := s.slots["cam-1"]; st.onAir != OnAirUnknown {
		t.Fatalf("a re-attaching source must reset stale on-air to %s, got %q", OnAirUnknown, st.onAir)
	}
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("a re-attaching source must degrade the occupant's pill (D-24), got %q (out=%+v)", got, out)
	}
}

// D-24: a participant joining/rejoining mid-stream gets the room's current OBS reflections —
// the global "we're live" state (room-level {t:streaming}), and the on-air of any slot it
// already occupies folded into its fresh roster (an eviction-rejoin keeps its binding) — so
// it doesn't sit at the defaults until OBS next toggles. OBS source virtual peers get neither
// (EN-13). The interim per-slot {t:onair} replay is retired.
func TestJoinReplaysCurrentObsState(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, 1) // slot on-air
	s.obsStreaming(true)                // broadcast live

	// A new participant joining mid-stream is told the broadcast is live.
	out := s.join("h", "host", "")
	var hStreaming bool
	for _, o := range out {
		if o.to == "h" && o.frame.T == "streaming" && o.frame.Active {
			hStreaming = true
		}
	}
	if !hStreaming {
		t.Fatalf("a participant joining mid-stream must be replayed the live state, got %+v", out)
	}

	// An OBS source page joining is NOT replayed participant reflections (EN-13).
	out = s.join("src2", "obs", "")
	for _, o := range out {
		if o.frame.T == "streaming" || o.frame.T == "roster" || o.frame.T == "onair" {
			t.Fatalf("OBS source pages must not receive streaming/roster replays, got %+v", o.frame)
		}
	}

	// The occupant rejoining (eviction keeps its binding) gets its slot's on-air folded into the
	// fresh roster — its self entry shows on-air, no separate {t:onair} frame.
	out = s.join("g1", "guest", "")
	for _, o := range framesTo(out, "g1") {
		if o.T == "onair" {
			t.Fatalf("the interim {t:onair} frame must be retired; on-air rides the roster, got %+v", o)
		}
	}
	e, ok := rosterEntryFor(out, "g1", "g1")
	if !ok || !e.Self || e.OnAir != OnAirYes {
		t.Fatalf("a rejoining occupant's roster self entry must show on-air, got %+v (out=%+v)", e, out)
	}
}

// D-24/EN-3: a source reattach (eviction reload — Room.Join swaps the conn without running
// leave) bumps the epoch, so an in-flight sourceActive from the EVICTED connection — carrying
// the old epoch — is rejected and can't re-light a stale pill after the reset.
func TestReattachBumpsEpochInvalidatingStaleReports(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")         // epoch 1
	s.obsSourceActive("cam-1", true, 1) // on-air at epoch 1
	oldEpoch := s.slots["cam-1"].epoch

	s.attachSource("cam-1", "src") // the source page reloads → reattach (same peer id)
	if s.slots["cam-1"].epoch == oldEpoch {
		t.Fatalf("a source reattach must bump the epoch to invalidate stale reports, still %d", oldEpoch)
	}

	// A stale sourceActive from the evicted connection (old epoch) must be ignored, leaving the
	// pill at the status-unavailable the reattach reset it to.
	if out := s.obsSourceActive("cam-1", true, oldEpoch); out != nil {
		t.Fatalf("a stale sourceActive at the old epoch must be ignored after reattach, got %+v", out)
	}
	if s.slots["cam-1"].onAir != OnAirUnknown {
		t.Fatalf("a stale report must NOT re-light the pill after reattach; on-air = %q", s.slots["cam-1"].onAir)
	}
}

func TestRelaySignalToKnownPeerOnly(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest", "")
	s.join("b", "guest", "")

	out := s.relaySignal("a", Frame{T: "signal", To: "b", SDP: []byte(`{"x":1}`)})
	if len(out) != 1 || out[0].to != "b" || out[0].frame.From != "a" || out[0].frame.To != "" {
		t.Fatalf("relay = %+v, want one frame to b stamped from=a", out)
	}
	if got := s.relaySignal("a", Frame{T: "signal", To: "ghost"}); got != nil {
		t.Fatalf("relay to unknown peer must drop, got %+v", got)
	}

	s.leave("b")
	if got := s.relaySignal("a", Frame{T: "signal", To: "b"}); got != nil {
		t.Fatalf("relay to a departed peer must drop, got %+v", got)
	}
}

// The relayed frame carries ONLY the opaque payload (sdp/ice) stamped with the sender —
// a peer must not be able to inject roster/slot/control fields into a frame the addressee
// will act on (D-23: the server relays SDP/ICE, nothing else).
func TestRelaySignalStripsExtraneousFields(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest", "")
	s.join("b", "guest", "")

	sdp := []byte(`{"type":"offer","sdp":"v=0..."}`)
	in := Frame{
		T: "signal", To: "b", SDP: sdp,
		// Hostile extras a client must not be able to smuggle through the relay:
		Slot: "cam-1", Epoch: epochPtr(9), Reason: "kicked", OnAir: "on-air",
		OccupantPeerID: "x", Event: "sourceActive", Active: true,
		Cam: true, Mic: true, Screen: true,
		Peers:  []RosterEntry{{ID: "fake", Role: "host"}},
		Peer:   &RosterEntry{ID: "fake", Role: "host"},
		PeerID: "fake",
	}
	out := s.relaySignal("a", in)
	if len(out) != 1 || out[0].to != "b" {
		t.Fatalf("relay = %+v, want one frame to b", out)
	}
	got := out[0].frame
	if got.T != "signal" || got.From != "a" || string(got.SDP) != string(sdp) {
		t.Fatalf("relayed core = %+v, want signal/from=a/same sdp", got)
	}
	if got.To != "" || got.Slot != "" || got.Epoch != nil || got.Reason != "" || got.OnAir != "" ||
		got.OccupantPeerID != "" || got.Event != "" || got.Active || got.Cam || got.Mic || got.Screen ||
		got.Peers != nil || got.Peer != nil || got.PeerID != "" {
		t.Fatalf("relayed frame leaked client-supplied fields: %+v", got)
	}
}

// epochVal dereferences a frame's epoch for assertions, returning -1 when absent (so a
// missing epoch never accidentally equals a real one).
func epochVal(f Frame) int {
	if f.Epoch == nil {
		return -1
	}
	return *f.Epoch
}

// On the wire, epoch rides ONLY slot frames: a relayed signal omits it entirely, while a
// slot-unbound carries it even at epoch 0 (EN-3).
func TestEpochOnlySerializesForSlotFrames(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest", "")
	s.join("b", "guest", "")

	relayed := s.relaySignal("a", Frame{T: "signal", To: "b", SDP: []byte(`"x"`)})
	if b, _ := json.Marshal(relayed[0].frame); strings.Contains(string(b), "epoch") {
		t.Fatalf("a relayed signal must not carry epoch on the wire, got %s", b)
	}

	s.join("src", "obs", "")
	out := s.attachSource("cam-1", "src") // fresh slot → slot-unbound at epoch 0
	b, _ := json.Marshal(out[0].frame)
	if !strings.Contains(string(b), `"epoch":0`) {
		t.Fatalf("a slot frame must carry epoch even at 0, got %s", b)
	}
}

// SDP and ICE are opaque (json.RawMessage): the server relays them byte-for-byte and
// never parses them (D-23). An ICE-only frame relays just as an SDP one does.
func TestRelaySignalRelaysPayloadVerbatim(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest", "")
	s.join("b", "guest", "")

	// An ICE candidate the server has no schema for — relayed unchanged.
	ice := []byte(`{"candidate":"candidate:1 1 udp 2122260223 192.0.2.1 54321 typ host","sdpMid":"0","sdpMLineIndex":0}`)
	out := s.relaySignal("a", Frame{T: "signal", To: "b", ICE: ice})
	if len(out) != 1 || string(out[0].frame.ICE) != string(ice) || out[0].frame.From != "a" {
		t.Fatalf("ICE relay = %+v, want byte-identical ice stamped from=a", out)
	}
	// A payload that isn't even a JSON object (the server never inspects shape).
	weird := []byte(`"just-a-string"`)
	out = s.relaySignal("a", Frame{T: "signal", To: "b", SDP: weird})
	if len(out) != 1 || string(out[0].frame.SDP) != string(weird) {
		t.Fatalf("opaque sdp relay = %+v, want byte-identical %s", out, weird)
	}
}

// Leaving while occupying a slot unbinds it (EN-3) so the source falls to placeholder.
func TestLeaveUnbindsOccupiedSlot(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")

	out := s.leave("g1")
	st := s.slots["cam-1"]
	if st.occupant != "" || st.epoch != 2 {
		t.Fatalf("leave should unbind the slot, got %+v", st)
	}
	found := false
	for _, o := range out {
		if o.frame.T == "slot-unbound" {
			found = true
		}
	}
	if !found {
		t.Fatalf("leave should emit slot-unbound to the source, got %+v", out)
	}
}
