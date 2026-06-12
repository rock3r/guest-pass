package signaling

import "testing"

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
		out[0].frame.OccupantPeerID != "g1" || out[0].frame.Epoch != 1 {
		t.Fatalf("outbound = %+v, want one slot-rebind(g1, epoch 1) to src", out)
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
	if len(out) != 1 || out[0].frame.T != "slot-unbound" || out[0].frame.Epoch != 2 {
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
