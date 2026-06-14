package signaling

import (
	"encoding/json"
	"strings"
	"testing"
)

// EN-3: a rebind bumps the epoch, swaps the occupant, resets on-air to unknown, and
// tells the source page to renegotiate to the new occupant at the new epoch.
func TestRebindBumpsEpochAndResetsOnAir(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest")

	out := s.rebindSlot("cam-1", "g1")

	st := s.slots["cam-1"]
	if st.epoch != 1 || st.occupant != "g1" || st.onAir != OnAirUnknown {
		t.Fatalf("slot state = %+v, want epoch 1 / g1 / %s", st, OnAirUnknown)
	}
	if len(out) != 1 || out[0].to != "src" || out[0].frame.T != "slot-rebind" ||
		out[0].frame.OccupantPeerID != "g1" || epochVal(out[0].frame) != 1 {
		t.Fatalf("outbound = %+v, want one slot-rebind(g1, epoch 1) to src", out)
	}
}

// D-24: a source's streamingStarted/Stopped reflection broadcasts a GLOBAL "we're live"
// state to every participant (host/co-host/guest) — but never to OBS source virtual peers
// (EN-13) — and is not epoch-scoped.
func TestObsStreamingBroadcastsToParticipantsOnly(t *testing.T) {
	s := newRoomState()
	s.join("host", "host")
	s.join("g1", "guest")
	s.join("src-cam-1", "obs") // a source page — must NOT receive the streaming broadcast

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

// D-24: when the OBS source for a slot disconnects, its on-air reflection is gone — the slot
// must degrade to status-unavailable and tell the occupant, never leave it asserting a stale
// on-air with no live OBS signal behind it ("never assert when unknown").
func TestSourceLeaveResetsOnAirToUnavailable(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest")
	s.rebindSlot("cam-1", "g1")         // epoch 1, occupant g1
	s.obsSourceActive("cam-1", true, 1) // slot reflects on-air
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("precondition: slot should be on-air, got %q", s.slots["cam-1"].onAir)
	}

	out := s.leave("src") // the OBS source disconnects

	if st := s.slots["cam-1"]; st.onAir != OnAirUnknown {
		t.Fatalf("on-air must degrade to %s when the source leaves, got %q", OnAirUnknown, st.onAir)
	}
	var told bool
	for _, o := range out {
		if o.to == "g1" && o.frame.T == "onair" && o.frame.OnAir == OnAirUnknown {
			told = true
		}
	}
	if !told {
		t.Fatalf("occupant g1 must be told {t:onair, status-unavailable} on source leave, got %+v", out)
	}
}

// EN-3 (the keystone): after a rebind, a STALE obsSourceActive carrying the previous
// epoch must NOT light the new occupant; only the current epoch's event applies.
func TestStaleObsActiveIgnoredAfterRebind(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest")
	s.rebindSlot("cam-1", "g1") // epoch 1

	if out := s.obsSourceActive("cam-1", true, 1); len(out) != 1 || out[0].frame.OnAir != OnAirYes {
		t.Fatalf("epoch-1 active should light g1, got %+v", out)
	}

	s.join("g2", "guest")
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
	s.join("g1", "guest")
	s.rebindSlot("cam-1", "g1") // bound before any source attaches

	s.join("src", "obs")
	out := s.attachSource("cam-1", "src")
	if len(out) != 1 || out[0].frame.T != "slot-rebind" || out[0].frame.OccupantPeerID != "g1" {
		t.Fatalf("attach should deliver the current binding, got %+v", out)
	}
}

// A rebind naming a peer that isn't in the room is a no-op: it must not advance the
// epoch or bind the slot to a peer that can't receive media/on-air.
func TestRebindToUnknownPeerIsNoOp(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs")
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
	s.join("src", "obs")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest")
	s.rebindSlot("cam-1", "g1")

	out := s.unbindSlot("cam-1")
	st := s.slots["cam-1"]
	if st.epoch != 2 || st.occupant != "" || st.onAir != OnAirUnknown {
		t.Fatalf("unbind state = %+v", st)
	}
	if len(out) != 1 || out[0].frame.T != "slot-unbound" || epochVal(out[0].frame) != 2 {
		t.Fatalf("unbind outbound = %+v", out)
	}
}

func TestRelaySignalToKnownPeerOnly(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest")
	s.join("b", "guest")

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
	s.join("a", "guest")
	s.join("b", "guest")

	sdp := []byte(`{"type":"offer","sdp":"v=0..."}`)
	in := Frame{
		T: "signal", To: "b", SDP: sdp,
		// Hostile extras a client must not be able to smuggle through the relay:
		Slot: "cam-1", Epoch: epochPtr(9), Reason: "kicked", OnAir: "on-air",
		OccupantPeerID: "x", Event: "sourceActive", Active: true,
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
		got.OccupantPeerID != "" || got.Event != "" || got.Active ||
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
	s.join("a", "guest")
	s.join("b", "guest")

	relayed := s.relaySignal("a", Frame{T: "signal", To: "b", SDP: []byte(`"x"`)})
	if b, _ := json.Marshal(relayed[0].frame); strings.Contains(string(b), "epoch") {
		t.Fatalf("a relayed signal must not carry epoch on the wire, got %s", b)
	}

	s.join("src", "obs")
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
	s.join("a", "guest")
	s.join("b", "guest")

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
	s.join("src", "obs")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest")
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
