package signaling

import "testing"

// hasFrameOfType reports whether any outbound carries a frame of type t (for asserting a frame is
// NOT emitted — e.g. an occupant-locks for a peer who sources no slot).
func hasFrameOfType(out []outbound, t string) bool {
	for _, o := range out {
		if o.frame.T == t {
			return true
		}
	}
	return false
}

// lockKindsContains reports whether kinds includes kind.
func lockKindsContains(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// RF-8 (receiver-side, OBS source half): an OBS source page receives no roster (EN-13), so a force
// on its bound occupant must reach it via a dedicated {t:occupant-locks} frame carrying the locked
// KINDS + the slot epoch/occupant — so the source detaches the locked remote track from the program
// output independent of the (possibly modified) occupant. Release re-emits with the kind gone.
func TestForceAndReleaseEmitOccupantLocksToSource(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1") // bound at epoch 1

	out := s.force("host", "g1", "mic")
	f, ok := firstFrameOfType(out, "src", "occupant-locks")
	if !ok {
		t.Fatalf("force on a sourced occupant must emit occupant-locks to the source, got %+v", out)
	}
	if f.OccupantPeerID != "g1" || epochVal(f) != 1 {
		t.Fatalf("occupant-locks must carry the occupant + slot epoch, got occupant=%q epoch=%d", f.OccupantPeerID, epochVal(f))
	}
	if !lockKindsContains(f.LockKinds, "mic") {
		t.Fatalf("occupant-locks must carry the locked kind mic, got %v", f.LockKinds)
	}

	// The kinds-only projection must NOT leak applier identity/rank to the crown-jewel source (EN-5).
	if f.PeerID != "" || f.Role != "" {
		t.Fatalf("occupant-locks must not carry applier identity/rank, got peerId=%q role=%q", f.PeerID, f.Role)
	}

	out = s.release("host", "g1", "mic")
	f, ok = firstFrameOfType(out, "src", "occupant-locks")
	if !ok {
		t.Fatalf("release must re-emit occupant-locks to the source, got %+v", out)
	}
	if lockKindsContains(f.LockKinds, "mic") {
		t.Fatalf("released mic must be gone from the occupant-locks kinds, got %v", f.LockKinds)
	}
}

// RF-8: a source attaching to an already-bound, already-locked occupant must learn the lock
// immediately (so a force-then-source-connects, or a reconnect after a seeded lock, detaches at once).
func TestAttachSourceToLockedOccupantEmitsOccupantLocks(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1") // bound at epoch 1, no source yet
	s.force("host", "g1", "cam")

	s.join("src", "obs", "")
	out := s.attachSource("cam-1", "src")
	if _, ok := firstFrameOfType(out, "src", "slot-rebind"); !ok {
		t.Fatalf("attach should still deliver the slot binding, got %+v", out)
	}
	f, ok := firstFrameOfType(out, "src", "occupant-locks")
	if !ok {
		t.Fatalf("attach to a locked occupant must deliver occupant-locks, got %+v", out)
	}
	if f.OccupantPeerID != "g1" || epochVal(f) != 1 || !lockKindsContains(f.LockKinds, "cam") {
		t.Fatalf("occupant-locks on attach = occupant %q epoch %d kinds %v, want g1/1/[cam]", f.OccupantPeerID, epochVal(f), f.LockKinds)
	}
}

// RF-8: a rebind to a NEW occupant re-projects the new occupant's locks to the source (so a stale
// lock view from the previous occupant is replaced — an unlocked new occupant clears the source's set).
func TestRebindReprojectsOccupantLocksForNewOccupant(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")
	s.rebindSlot("cam-1", "g1") // epoch 1
	s.force("host", "g1", "mic")

	out := s.rebindSlot("cam-1", "g2") // epoch 2, new occupant
	f, ok := firstFrameOfType(out, "src", "occupant-locks")
	if !ok {
		t.Fatalf("rebind to a new occupant must re-project occupant-locks, got %+v", out)
	}
	if f.OccupantPeerID != "g2" || epochVal(f) != 2 {
		t.Fatalf("occupant-locks after rebind = occupant %q epoch %d, want g2/2", f.OccupantPeerID, epochVal(f))
	}
	if len(f.LockKinds) != 0 {
		t.Fatalf("an unlocked new occupant must clear the source's lock kinds, got %v", f.LockKinds)
	}
}

// RF-8: a force on a peer who occupies no sourced slot emits only the roster — no occupant-locks
// (there is no source to enforce it for).
func TestForceOnNonSourcedOccupantEmitsNoOccupantLocks(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")

	out := s.force("host", "g1", "mic")
	if hasFrameOfType(out, "occupant-locks") {
		t.Fatalf("a force on a peer sourcing no slot must not emit occupant-locks, got %+v", out)
	}
}

// RF-8: attaching a source to an UNBOUND slot delivers slot-unbound and NO occupant-locks (there is
// no occupant to lock).
func TestAttachUnboundSlotEmitsNoOccupantLocks(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	out := s.attachSource("cam-1", "src")
	if _, ok := firstFrameOfType(out, "src", "slot-unbound"); !ok {
		t.Fatalf("attaching to an unbound slot should deliver slot-unbound, got %+v", out)
	}
	if hasFrameOfType(out, "occupant-locks") {
		t.Fatalf("an unbound slot has no occupant — must emit no occupant-locks, got %+v", out)
	}
}
