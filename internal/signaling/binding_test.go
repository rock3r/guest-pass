package signaling

import "testing"

// entryByID returns the roster entry for id, or fails.
func entryByID(t *testing.T, entries []RosterEntry, id string) RosterEntry {
	t.Helper()
	for _, e := range entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no roster entry for %q", id)
	return RosterEntry{}
}

// AC-6 (D-15/D-20): the slot a participant occupies is surfaced in the HOST's roster
// projection (boundSlot) so the greenroom People controls can show + change it — and is
// STRIPPED from co-host and guest projections (slot bindings are host-only).
func TestRosterBoundSlotHostOnly(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "Greta")
	s.rebindSlot("cam-1", "g1")

	if e := entryByID(t, s.rosterFor("host", "host"), "g1"); e.BoundSlot != "cam-1" {
		t.Fatalf("host sees g1 boundSlot=%q, want cam-1", e.BoundSlot)
	}
	for _, rr := range []struct{ id, role string }{{"co", "cohost"}, {"g1", "guest"}} {
		if e := entryByID(t, s.rosterFor(PeerID(rr.id), rr.role), "g1"); e.BoundSlot != "" {
			t.Fatalf("%s sees boundSlot=%q, want it stripped (host-only)", rr.role, e.BoundSlot)
		}
	}

	// A move to cam-2 (the real flow vacates the old slot first, then binds the new) updates
	// the host's view to the single occupied cam slot.
	s.unbindSlot("cam-1")
	s.rebindSlot("cam-2", "g1")
	if e := entryByID(t, s.rosterFor("host", "host"), "g1"); e.BoundSlot != "cam-2" {
		t.Fatalf("after move, host sees boundSlot=%q, want cam-2", e.BoundSlot)
	}

	// Unbinding clears it.
	s.unbindSlot("cam-2")
	if e := entryByID(t, s.rosterFor("host", "host"), "g1"); e.BoundSlot != "" {
		t.Fatalf("after unbind, host sees boundSlot=%q, want empty", e.BoundSlot)
	}
}

// rebindOrVacate binds to a connected occupant, but VACATES the slot when the new occupant is
// OFFLINE — so a swap whose new guest hasn't connected drops the slot to placeholder rather
// than stranding the displaced prior occupant live (cursor, M4 PR-6).
func TestRebindOrVacateOfflineOccupant(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("a", "guest", "")

	// Bind cam-1 to A (a connected peer) → A occupies it.
	s.rebindOrVacate("cam-1", "a")
	if s.slot("cam-1").occupant != "a" {
		t.Fatalf("rebindOrVacate(A) occupant = %q, want a", s.slot("cam-1").occupant)
	}

	// Reassign cam-1 to B, who is OFFLINE → the slot is VACATED (A displaced), NOT left on A.
	s.rebindOrVacate("cam-1", "b")
	if occ := s.slot("cam-1").occupant; occ != "" {
		t.Fatalf("offline reassign kept occupant %q, want the slot vacated (placeholder)", occ)
	}

	// Binding to a connected peer C binds it for real.
	s.join("c", "guest", "")
	s.rebindOrVacate("cam-1", "c")
	if s.slot("cam-1").occupant != "c" {
		t.Fatalf("rebindOrVacate(C) occupant = %q, want c", s.slot("cam-1").occupant)
	}
}

// vacateOccupant (greenroom unassign) clears whatever cam slot the guest holds live — keyed on
// occupancy, not a caller label — so a concurrent move can't leave a stale slot bound (codex).
func TestVacateOccupantClearsLiveSlot(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g", "guest", "")
	s.rebindSlot("cam-3", "g")

	// Unassign keyed on the peer: it clears cam-3 even if the caller thought g was elsewhere.
	s.vacateOccupant("g")
	if occ := s.slot("cam-3").occupant; occ != "" {
		t.Fatalf("cam-3 occupant = %q after vacateOccupant, want empty", occ)
	}
	// Idempotent: vacating an unbound guest is a no-op.
	if out := s.vacateOccupant("g"); len(out) != 0 {
		t.Fatalf("vacateOccupant on an unbound guest emitted %d frames, want 0", len(out))
	}
}

// One cam slot per occupant (codex, M4 PR-6): moving a guest to a new cam slot VACATES the old
// one in the same rebind, so a guest is never live in two OBS sources while the DB stores only
// the last slot (the failure a concurrent unassigned→cam-1→cam-2 race would otherwise cause).
func TestRebindEnforcesOneCamSlotPerOccupant(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g", "guest", "")

	s.rebindSlot("cam-1", "g")
	if s.slot("cam-1").occupant != "g" {
		t.Fatalf("cam-1 occupant = %q, want g", s.slot("cam-1").occupant)
	}
	// Move g to cam-2 WITHOUT an explicit unbind: the reducer frees cam-1 itself.
	s.rebindSlot("cam-2", "g")
	if s.slot("cam-2").occupant != "g" {
		t.Fatalf("cam-2 occupant = %q, want g", s.slot("cam-2").occupant)
	}
	if occ := s.slot("cam-1").occupant; occ != "" {
		t.Fatalf("g still in cam-1 after moving to cam-2 (occupant %q) — two slots at once", occ)
	}
}
