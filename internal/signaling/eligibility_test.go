package signaling

import "testing"

// hasLockKind reports whether a roster entry carries a lock of the given kind.
func hasLockKind(e RosterEntry, kind string) bool {
	for _, l := range e.Locks {
		if l.Kind == kind {
			return true
		}
	}
	return false
}

// AC-9/EN-23: screenshare eligibility (can_screen) is HOST-managed policy — the host sees every
// guest's eligibility (for the grant/revoke toggle) and a guest sees only its OWN (its share
// affordance). It is stripped from a non-host's view of OTHER participants.
func TestScreenEligibilityHostOnlyProjection(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")

	if out := s.setScreenEligible("g1", true); out == nil {
		t.Fatal("granting eligibility should re-broadcast the roster")
	}
	if e := entryByID(t, s.rosterFor("host", "host"), "g1"); !e.CanScreen {
		t.Fatal("host must see g1's eligibility")
	}
	if e := entryByID(t, s.rosterFor("g1", "guest"), "g1"); !e.CanScreen {
		t.Fatal("g1 must see its OWN eligibility (its share affordance)")
	}
	if e := entryByID(t, s.rosterFor("g2", "guest"), "g1"); e.CanScreen {
		t.Fatal("g2 must NOT see g1's eligibility (host policy, EN-7)")
	}

	// No-op for an unchanged value or an absent peer.
	if out := s.setScreenEligible("g1", true); out != nil {
		t.Fatalf("re-granting an already-eligible guest must be a no-op, got %v", out)
	}
	if out := s.setScreenEligible("ghost", true); out != nil {
		t.Fatalf("eligibility for an absent peer must be a no-op, got %v", out)
	}
}

// AC-9: a live REVOKE runs the force-no-share path — it applies a host-authority share suppression
// lock (visible in the roster, RF-8 to the source), suppresses the screen presence at source, and
// clears eligibility. A connected host peer is NOT required (it is server policy).
func TestScreenEligibilityRevokeAppliesForceNoShare(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.setScreenEligible("g1", true)
	s.peers["g1"].screen = true // pretend g1 is actively sharing

	s.setScreenEligibleLive("g1", false)
	if !s.locked("g1", "share") {
		t.Fatal("a revoke must apply the force-no-share lock")
	}
	if s.peers["g1"].screen {
		t.Fatal("a revoke must suppress the share presence at source")
	}
	e := entryByID(t, s.rosterFor("host", "host"), "g1")
	if e.CanScreen {
		t.Fatal("a revoked guest must not be eligible")
	}
	if !hasLockKind(e, "share") {
		t.Fatal("the share lock must appear in the roster")
	}

	// A re-revoke is a no-op on the already-host-locked share (idempotent).
	before := s.lockOn("g1", "share")
	s.setScreenEligibleLive("g1", false)
	if s.lockOn("g1", "share") != before {
		t.Fatal("re-revoking must not replace the existing host share lock")
	}

	// GRANT clears the host-applied share lock (the guest may share again).
	s.setScreenEligibleLive("g1", true)
	if s.locked("g1", "share") {
		t.Fatal("a grant must clear the eligibility share lock")
	}
	if e := entryByID(t, s.rosterFor("g1", "guest"), "g1"); !e.CanScreen {
		t.Fatal("g1 must see itself eligible again after the grant")
	}
}

// AC-9: a grant must NOT clear a CO-HOST's independent moderation force-no-share (lower floor) — only
// the host-floor eligibility lock. Moderation and eligibility are separate authorities.
func TestScreenEligibilityGrantLeavesCohostLock(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")

	// A co-host force-no-shares g1 (moderation, floor = cohost).
	s.force("co", "g1", "share")
	if !s.locked("g1", "share") {
		t.Fatal("co-host force-no-share should lock g1's share")
	}
	// The host grants eligibility → must leave the co-host's moderation lock in place.
	s.setScreenEligibleLive("g1", true)
	if !s.locked("g1", "share") {
		t.Fatal("a grant must not clear a co-host's moderation share lock")
	}
}

// AC-9 (codex HIGH): a host REVOKE must not OVERWRITE a co-host's existing force-no-share lock with
// a host-floor one — otherwise a later grant (which clears only host-floor locks) would erase the
// co-host's moderation entirely and let the guest share again despite the co-host never releasing.
func TestScreenEligibilityRevokeKeepsCohostLock(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")
	s.setScreenEligible("g1", true)

	// A co-host force-no-shares g1 (moderation, floor = cohost).
	s.force("co", "g1", "share")
	if lk := s.lockOn("g1", "share"); lk == nil || lk.floor != rankCohost {
		t.Fatalf("co-host force should leave a cohost-floor share lock, got %+v", lk)
	}
	// Host revokes eligibility → must NOT overwrite the co-host's lock (it stays cohost-floor).
	s.setScreenEligibleLive("g1", false)
	if lk := s.lockOn("g1", "share"); lk == nil || lk.floor != rankCohost {
		t.Fatalf("revoke overwrote the co-host's share lock (floor now %v) — must leave it", lk)
	}
	// Host grants eligibility again → the co-host's moderation lock SURVIVES (grant clears only the
	// host-floor eligibility lock, which was never applied here).
	s.setScreenEligibleLive("g1", true)
	if lk := s.lockOn("g1", "share"); lk == nil || lk.floor != rankCohost {
		t.Fatalf("revoke→grant erased the co-host's moderation lock (got %+v) — it must survive", lk)
	}
}
