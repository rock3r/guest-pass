package signaling

import "testing"

// lockInEntry returns a roster entry's lock of the given kind, or false.
func lockInEntry(e RosterEntry, kind string) (LockView, bool) {
	for _, l := range e.Locks {
		if l.Kind == kind {
			return l, true
		}
	}
	return LockView{}, false
}

// AC-3/T-3: a force creates ONE lock per (target, modality), suppresses the matching presence
// at source, and the lock is live-visible in the roster (to the host AND the target itself)
// with applierRank.
func TestForceMuteLocksSuppressesAndShowsInRoster(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyState("g1", nil, bptr(true), nil, nil) // g1 unmuted

	out := s.force("host", "g1", "mic")

	if s.peers["g1"].mic {
		t.Fatalf("force-mute must suppress g1's mic at source")
	}
	for _, viewer := range []PeerID{"host", "g1"} {
		e, ok := rosterEntryFor(out, viewer, "g1")
		if !ok {
			t.Fatalf("%s should get a roster carrying g1, got %+v", viewer, out)
		}
		l, ok := lockInEntry(e, "mic")
		if !ok || l.ApplierRank != "host" || l.ApplierPeerID != "host" {
			t.Fatalf("%s should see g1's mic lock by host, got locks=%+v", viewer, e.Locks)
		}
		if e.Mic {
			t.Fatalf("%s should see g1's mic suppressed in the roster", viewer)
		}
	}
}

// T-3: a higher-rank force on a locked modality RAISES the floor + owner.
func TestHigherRankForceRaisesFloor(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")

	s.force("co", "g1", "mic") // cohost-floor lock owned by co
	if lk := s.peers["g1"].locks["mic"]; lk == nil || lk.floor != rankCohost || lk.applier != "co" {
		t.Fatalf("cohost force should set a cohost-floor lock owned by co, got %+v", lk)
	}
	s.force("host", "g1", "mic") // host raises
	if lk := s.peers["g1"].locks["mic"]; lk == nil || lk.floor != rankHost || lk.applier != "host" {
		t.Fatalf("a higher-rank force must raise the floor + owner, got %+v", lk)
	}
}

// T-3: a lower-OR-equal-rank force on an already-locked modality is a NO-OP (no ownership steal).
func TestLowerOrEqualForceIsNoOp(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")
	s.force("host", "g1", "mic") // host floor
	if out := s.force("co", "g1", "mic"); out != nil {
		t.Fatalf("a lower-rank force on a locked modality must be a no-op, got %+v", out)
	}
	if lk := s.peers["g1"].locks["mic"]; lk.floor != rankHost || lk.applier != "host" {
		t.Fatalf("the host lock must be unchanged after a lower-rank force, got %+v", lk)
	}

	s2 := newRoomState()
	s2.join("co", "cohost", "")
	s2.join("co2", "cohost", "")
	s2.join("g1", "guest", "")
	s2.force("co", "g1", "mic")
	if out := s2.force("co2", "g1", "mic"); out != nil {
		t.Fatalf("an equal-rank force on a locked modality must be a no-op, got %+v", out)
	}
	if lk := s2.peers["g1"].locks["mic"]; lk.applier != "co" {
		t.Fatalf("an equal-rank force must not steal ownership, got %+v", lk)
	}
}

// T-3: a force requires the actor be STRICTLY above the target — the host is immune, a co-host
// can't force a peer co-host or the host, and a guest can't force anyone. The host CAN force
// co-hosts and guests.
func TestForceRequiresStrictlyAbove(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("co2", "cohost", "")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")

	if out := s.force("co", "host", "mic"); out != nil || s.peers["host"].locked("mic") {
		t.Fatalf("a co-host must not force the host (immune)")
	}
	if out := s.force("co", "co2", "mic"); out != nil || s.peers["co2"].locked("mic") {
		t.Fatalf("a co-host must not force an equal-rank co-host")
	}
	if out := s.force("g1", "g2", "mic"); out != nil || s.peers["g2"].locked("mic") {
		t.Fatalf("a guest must not be able to force")
	}
	if out := s.force("host", "co", "cam"); out == nil || !s.peers["co"].locked("cam") {
		t.Fatalf("the host must be able to force a co-host")
	}
}

// AC-3/T-3: the target can NEVER self-release, and a self-state re-enabling a force-suppressed
// modality is REJECTED with an authoritative re-broadcast to the violating target only (its UI
// snaps back), without churning the roster for everyone else.
func TestTargetCannotSelfReleaseOrSelfEnable(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyState("g1", nil, bptr(true), nil, nil)
	s.force("host", "g1", "mic")

	if out := s.release("g1", "g1", "mic"); out != nil || !s.peers["g1"].locked("mic") {
		t.Fatalf("the target must not be able to self-release, got %+v", out)
	}

	out := s.applyState("g1", nil, bptr(true), nil, nil) // try to self-unmute
	if s.peers["g1"].mic {
		t.Fatalf("a force-muted target must not be able to self-enable mic")
	}
	e, ok := rosterEntryFor(out, "g1", "g1")
	if !ok || e.Mic {
		t.Fatalf("the violating target must get an authoritative roster with mic suppressed, got %+v", e)
	}
	if _, ok := firstFrameOfType(out, "host", "roster"); ok {
		t.Fatalf("a rejected self-state must not churn the roster for others")
	}
}

// AC-3/T-3: release authority is the actor's CURRENT rank ≥ the lock floor — the applier (at
// floor) and the host (≥ any floor) can; a DEMOTED applier that fell below the floor can NO
// longer release (the lock is not auto-released — demotion-safe), but the host always can.
func TestReleaseAuthorityAndDemotionSafe(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")

	s.force("co", "g1", "mic")
	if out := s.release("co", "g1", "mic"); out == nil || s.peers["g1"].locked("mic") {
		t.Fatalf("the applier (co) should be able to release its own lock")
	}

	s.force("co", "g1", "cam") // cohost-floor lock
	if out := s.release("host", "g1", "cam"); out == nil || s.peers["g1"].locked("cam") {
		t.Fatalf("the host must always be able to release")
	}

	// Demotion-safe: the floor persists, the now-guest ex-applier can't release, the host can.
	s.force("co", "g1", "share")
	s.peers["co"].role = "guest" // simulate a demotion (PR-5 path); floor must be unchanged
	if lk := s.peers["g1"].locks["share"]; lk == nil || lk.floor != rankCohost {
		t.Fatalf("the lock floor must persist across a demotion (not auto-released), got %+v", lk)
	}
	if out := s.release("co", "g1", "share"); out != nil || !s.peers["g1"].locked("share") {
		t.Fatalf("a demoted applier (now below the floor) must NOT release, got %+v", out)
	}
	if out := s.release("host", "g1", "share"); out == nil || s.peers["g1"].locked("share") {
		t.Fatalf("the host must still release a demoted applier's lock, got %+v", out)
	}
}

// T-3: "release by any rank ≥ floor" includes an equal-rank PEER — a co-host may release
// another co-host's cohost-floor lock; the releaser need not be the applier.
func TestEqualRankPeerCanRelease(t *testing.T) {
	s := newRoomState()
	s.join("co", "cohost", "")
	s.join("co2", "cohost", "")
	s.join("g1", "guest", "")
	s.force("co", "g1", "mic") // cohost-floor lock owned by co

	if out := s.release("co2", "g1", "mic"); out == nil || s.peers["g1"].locked("mic") {
		t.Fatalf("an equal-rank co-host peer should be able to release a cohost-floor lock, got %+v", out)
	}
}

// T-3: a legit change to an UNLOCKED modality alongside a rejected re-enable of a LOCKED one
// still applies the legit change and re-broadcasts to everyone, lock + suppression intact.
func TestStateLegitChangeAlongsideViolationRebroadcasts(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.force("host", "g1", "mic")

	out := s.applyState("g1", bptr(true), bptr(true), nil, nil) // cam on (ok), mic on (rejected)
	if s.peers["g1"].mic {
		t.Fatalf("the locked mic must stay suppressed")
	}
	if !s.peers["g1"].cam {
		t.Fatalf("the unlocked cam change must apply")
	}
	e, ok := rosterEntryFor(out, "host", "g1")
	if !ok || e.Mic || !e.Cam {
		t.Fatalf("host roster should show g1 cam on / mic suppressed, got %+v", e)
	}
	if _, ok := lockInEntry(e, "mic"); !ok {
		t.Fatalf("the mic lock must remain in the roster, got %+v", e.Locks)
	}
}

// AC-3 (reconnect invariant): a reconnect (eviction → rejoin with the same id) must NOT clear
// a suppression lock — otherwise a force-muted target could self-release just by reconnecting.
// The lock + its suppressed presence carry across the rejoin.
func TestRejoinPreservesLock(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyState("g1", nil, bptr(true), nil, nil)
	s.force("host", "g1", "mic") // g1 force-muted

	out := s.join("g1", "guest", "") // g1 reconnects (same id)
	if !s.peers["g1"].locked("mic") {
		t.Fatalf("a reconnect must not clear the suppression lock (would be a self-release)")
	}
	if s.peers["g1"].mic {
		t.Fatalf("a reconnecting force-muted peer must stay suppressed")
	}
	// Its fresh roster still shows the mic lock.
	if e, ok := rosterEntryFor(out, "g1", "g1"); !ok {
		t.Fatalf("the reconnecting peer should get a roster")
	} else if _, ok := lockInEntry(e, "mic"); !ok {
		t.Fatalf("the reconnect roster must still carry the mic lock, got %+v", e.Locks)
	}
	// And it still cannot self-unmute after reconnecting.
	s.applyState("g1", nil, bptr(true), nil, nil)
	if s.peers["g1"].mic {
		t.Fatalf("a reconnected force-muted peer must still not self-unmute")
	}
}

// T-3: releasing a non-existent lock is a no-op, and a guest (below any floor) cannot release.
func TestReleaseNoLockOrUnauthorized(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")

	if out := s.release("host", "g1", "mic"); out != nil {
		t.Fatalf("releasing a non-existent lock must be a no-op, got %+v", out)
	}
	s.force("host", "g1", "mic")
	if out := s.release("g2", "g1", "mic"); out != nil || !s.peers["g1"].locked("mic") {
		t.Fatalf("a guest must not release a host-floor lock, got %+v", out)
	}
}

// T-3: forces are per-modality independent — force-no-cam and force-no-share lock cam/share via
// the cam/screen presence, leaving other modalities untouched.
func TestForcesArePerModality(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyState("g1", bptr(true), bptr(true), bptr(true), nil) // cam+mic+screen on

	s.force("host", "g1", "cam")
	s.force("host", "g1", "share")
	p := s.peers["g1"]
	if p.cam || p.screen {
		t.Fatalf("force-no-cam/force-no-share must suppress cam + screen, got cam=%v screen=%v", p.cam, p.screen)
	}
	if !p.mic {
		t.Fatalf("an un-forced modality (mic) must stay as the guest set it")
	}
	if !p.locked("cam") || !p.locked("share") || p.locked("mic") {
		t.Fatalf("only cam + share must be locked, got locks=%v", p.locks)
	}
}
