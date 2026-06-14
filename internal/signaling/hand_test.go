package signaling

import "testing"

// handOf returns a peer's handRaised as projected in the roster delivered to `to`.
func handOf(out []outbound, to, peer PeerID) bool {
	e, _ := rosterEntryFor(out, to, peer)
	return e.HandRaised
}

// AC-7/T-7: a participant raises and lowers its OWN hand; the roster reflects handRaised to
// everyone (and to the raiser's own self entry).
func TestSetHandSelfRaiseLower(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")

	out := s.setHand("g1", "", true) // raise own (empty target = self)
	if !s.peers["g1"].handRaised {
		t.Fatalf("g1 should have raised its hand")
	}
	if !handOf(out, "host", "g1") || !handOf(out, "g1", "g1") {
		t.Fatalf("the roster must reflect g1's raised hand to host + self, got %+v", out)
	}

	out = s.setHand("g1", "g1", false) // lower own (explicit self target)
	if s.peers["g1"].handRaised {
		t.Fatalf("g1 should have lowered its hand")
	}
	if handOf(out, "host", "g1") {
		t.Fatalf("the roster must reflect g1's hand lowered, got %+v", out)
	}

	// A no-op (already in that state) emits nothing.
	if out := s.setHand("g1", "", false); out != nil {
		t.Fatalf("a no-op hand change must emit nothing, got %+v", out)
	}
}

// AC-7/T-7: the HOST may dismiss (lower) another participant's raised hand; a non-host cannot
// dismiss; and nobody can RAISE a hand on someone else's behalf.
func TestSetHandHostDismiss(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")
	s.setHand("g1", "", true) // g1 raises

	// A co-host cannot dismiss g1's hand.
	if out := s.setHand("co", "g1", false); out != nil || !s.peers["g1"].handRaised {
		t.Fatalf("a non-host must not dismiss another's hand, got %+v", out)
	}
	// Nobody can raise a hand for someone else (even the host).
	s.join("g2", "guest", "")
	if out := s.setHand("host", "g2", true); out != nil || s.peers["g2"].handRaised {
		t.Fatalf("a hand cannot be raised on someone else's behalf, got %+v", out)
	}
	// The host dismisses g1's hand.
	out := s.setHand("host", "g1", false)
	if s.peers["g1"].handRaised {
		t.Fatalf("the host should dismiss g1's hand")
	}
	if handOf(out, "g1", "g1") {
		t.Fatalf("the roster must reflect g1's hand dismissed, got %+v", out)
	}
	// Dismissing an already-lowered hand is a no-op.
	if out := s.setHand("host", "g1", false); out != nil {
		t.Fatalf("dismissing a lowered hand must emit nothing, got %+v", out)
	}
}

// AC-7/T-7: a raised hand auto-clears when the guest is PROMOTED to co-host (bringing them in
// makes the nudge moot) and when the guest LEAVES (its peerInfo is removed).
func TestHandAutoClearsOnPromoteAndLeave(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.setHand("g1", "", true)

	s.setRole("host", "g1", "cohost") // promote
	if s.peers["g1"].handRaised {
		t.Fatalf("a promoted guest's hand must auto-clear")
	}

	// And leaving removes the participant entirely (hand gone with it).
	s.join("g2", "guest", "")
	s.setHand("g2", "", true)
	s.leave("g2")
	if _, ok := s.peers["g2"]; ok {
		t.Fatalf("a left participant must be removed (its raised hand goes with it)")
	}
}

// EN-7: an OBS source virtual peer has no hand; a {t:hand} from it is dropped.
func TestSetHandFromNonParticipantIgnored(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	if out := s.setHand("src", "", true); out != nil {
		t.Fatalf("an OBS source must not be able to raise a hand, got %+v", out)
	}
}
