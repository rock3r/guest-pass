package signaling

// setHand sets a participant's hand-raise state — the soft "bring me in" nudge folded into the
// roster's handRaised. A participant raises or lowers its OWN hand (target = self, or empty).
// The HOST may DISMISS another participant's raised hand (target ≠ self, lower-only) — nobody
// can raise a hand on someone else's behalf. The "guest can lower their own; the host can
// dismiss" clearing semantics match the M3 plan default. Auto-clear on leave is implicit (the
// peerInfo is removed); auto-clear on promotion is handled in setRole. Returns the roster
// re-broadcast on a real change.
func (s *roomState) setHand(actor, target PeerID, raised bool) []outbound {
	a := s.peers[actor]
	if a == nil || !isParticipant(a.role) {
		return nil
	}
	if target == "" || target == actor {
		// Self: raise or lower one's own hand.
		if a.handRaised == raised {
			return nil
		}
		a.handRaised = raised
		return s.rebroadcastRoster()
	}
	// Dismissing another's hand: HOST-ONLY and LOWER-only (a raised:true targeting someone else
	// is rejected — you can't raise a hand for another participant).
	if raised || rankOf(a.role) != rankHost {
		return nil
	}
	t := s.peers[target]
	if t == nil || !isParticipant(t.role) || !t.handRaised {
		return nil
	}
	t.handRaised = false
	return s.rebroadcastRoster()
}
