package signaling

import "testing"

// roleSeenBy returns a peer's role as projected in the roster delivered to `to`.
func roleSeenBy(out []outbound, to, peer PeerID) string {
	e, _ := rosterEntryFor(out, to, peer)
	return e.Role
}

// AC-5/T-5: the host promotes a guest to co-host and demotes a co-host to guest; the roster
// reflects the new role to viewers (including the target itself).
func TestSetRolePromoteDemote(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")

	out := s.setRole("host", "g1", "cohost")
	if s.peers["g1"].role != "cohost" {
		t.Fatalf("host should promote g1 to cohost, got %q", s.peers["g1"].role)
	}
	if got := roleSeenBy(out, "host", "g1"); got != "cohost" {
		t.Fatalf("the roster should show g1 as cohost, got %q", got)
	}

	out = s.setRole("host", "g1", "guest")
	if s.peers["g1"].role != "guest" {
		t.Fatalf("host should demote g1 to guest, got %q", s.peers["g1"].role)
	}
	if got := roleSeenBy(out, "g1", "g1"); got != "guest" {
		t.Fatalf("the roster should show g1 demoted to guest, got %q", got)
	}
}

// T-5: promote/demote is HOST-ONLY — a co-host or guest cannot change roles; the host cannot
// change a host (target must be strictly below); an invalid role and a no-op are rejected.
func TestSetRoleAuthority(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")

	if out := s.setRole("co", "g1", "cohost"); out != nil || s.peers["g1"].role != "guest" {
		t.Fatalf("a co-host must not promote/demote (host-only, D-15)")
	}
	if out := s.setRole("g1", "co", "guest"); out != nil || s.peers["co"].role != "cohost" {
		t.Fatalf("a guest must not change roles")
	}
	if out := s.setRole("host", "host", "guest"); out != nil || s.peers["host"].role != "host" {
		t.Fatalf("the host cannot be demoted (target must be strictly below)")
	}
	if out := s.setRole("host", "g1", "host"); out != nil || s.peers["g1"].role != "guest" {
		t.Fatalf("an invalid target role must be rejected")
	}
	if out := s.setRole("host", "g1", "guest"); out != nil {
		t.Fatalf("a no-op role change (same role) must emit nothing")
	}
}

// AC-5/T-5: demotion-safe lock re-evaluation — a co-host applies a lock, then the HOST demotes
// that co-host to guest. The lock floor persists (not auto-released); the demoted ex-co-host can
// no longer release its own lock (current rank < floor), but the host always can.
func TestSetRoleDemotionReevaluatesLocks(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")
	s.force("co", "g1", "mic") // cohost-floor lock owned by co

	s.setRole("host", "co", "guest") // demote the applier
	if lk := s.lockOn("g1", "mic"); lk == nil || lk.floor != rankCohost {
		t.Fatalf("the lock floor must persist across the applier's demotion, got %+v", lk)
	}
	if out := s.release("co", "g1", "mic"); out != nil || !s.locked("g1", "mic") {
		t.Fatalf("a demoted ex-cohost (now a guest) must not release its own lock, got %+v", out)
	}
	if out := s.release("host", "g1", "mic"); out == nil || s.locked("g1", "mic") {
		t.Fatalf("the host must still release the demoted applier's lock")
	}
}

// AC-5/T-5 (the promotion edge): a guest force-muted by a co-host (floor cohost) is PROMOTED to
// co-host. Its rank now EQUALS the floor, but the explicit self-release guard still forbids
// self-release; a different co-host peer (≥ floor) can release it.
func TestSetRolePromotionDoesNotEnableSelfRelease(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("co2", "cohost", "")
	s.join("g1", "guest", "")
	s.force("co", "g1", "mic") // cohost-floor lock on the guest

	s.setRole("host", "g1", "cohost") // promote the muted guest
	if rankOf(s.peers["g1"].role) != rankCohost {
		t.Fatalf("precondition: g1 should be a cohost now")
	}
	if out := s.release("g1", "g1", "mic"); out != nil || !s.locked("g1", "mic") {
		t.Fatalf("a promoted target must STILL not self-release its own lock, got %+v", out)
	}
	if out := s.release("co2", "g1", "mic"); out == nil || s.locked("g1", "mic") {
		t.Fatalf("a peer co-host (≥ floor) should be able to release the cohost-floor lock")
	}
}
