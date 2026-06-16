package signaling

import "testing"

// D-16/AC-7 (nameplate): the OBS source page gets no roster (EN-13), so the bound occupant's
// display name must ride the {t:slot-rebind} frame. A bind to a named occupant delivers the name
// to the source so it can render the nameplate (escaped textContent, gated by a URL param).
func TestSlotRebindCarriesOccupantName(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "Greta")

	out := s.rebindSlot("cam-1", "g1")
	f, ok := firstFrameOfType(out, "src", "slot-rebind")
	if !ok {
		t.Fatalf("rebind must deliver slot-rebind to the source, got %+v", out)
	}
	if f.OccupantPeerID != "g1" || f.Name != "Greta" {
		t.Fatalf("slot-rebind = occupant %q name %q, want g1/Greta", f.OccupantPeerID, f.Name)
	}
}

// D-16: a source (re)attaching to an already-bound slot also learns the occupant's name on the
// binding frame (a source-page refresh after a name was set must show the current nameplate).
func TestAttachSourceCarriesOccupantName(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "Greta")
	s.rebindSlot("cam-1", "g1") // bound before any source attaches

	s.join("src", "obs", "")
	out := s.attachSource("cam-1", "src")
	f, ok := firstFrameOfType(out, "src", "slot-rebind")
	if !ok {
		t.Fatalf("attach to a bound slot must deliver slot-rebind, got %+v", out)
	}
	if f.Name != "Greta" {
		t.Fatalf("attach slot-rebind name = %q, want Greta", f.Name)
	}
}

// AC-7: a host name override updates the sticky name, re-broadcasts the roster, and re-sends
// {t:slot-rebind} to the occupied slot's source with the SAME occupant + SAME epoch (a name-only
// refresh — the source updates the nameplate WITHOUT re-linking media, no video flicker).
func TestSetNameRefreshesNameplateSameEpoch(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "Greta")
	s.rebindSlot("cam-1", "g1")
	epochAtBind := s.slot("cam-1").epoch

	out := s.setName("g1", "Margaret")
	f, ok := firstFrameOfType(out, "src", "slot-rebind")
	if !ok {
		t.Fatalf("setName on a sourced occupant must re-send slot-rebind, got %+v", out)
	}
	if f.OccupantPeerID != "g1" || f.Name != "Margaret" {
		t.Fatalf("refresh slot-rebind = occupant %q name %q, want g1/Margaret", f.OccupantPeerID, f.Name)
	}
	if epochVal(f) != epochAtBind {
		t.Fatalf("a name-only refresh must keep the SAME epoch (no media re-link): got %d, want %d", epochVal(f), epochAtBind)
	}
	if s.slot("cam-1").epoch != epochAtBind {
		t.Fatalf("setName must not bump the slot epoch, slot epoch = %d, want %d", s.slot("cam-1").epoch, epochAtBind)
	}
	// The roster also reflects the new sticky name for the greenroom.
	if e := entryByID(t, framesToRoster(t, out, "host"), "g1"); e.Name != "Margaret" {
		t.Fatalf("roster after setName has name %q, want Margaret", e.Name)
	}
}

// AC-7: a name override for a peer who occupies no sourced slot still updates the roster (the
// greenroom reflects it) but emits NO slot-rebind (there is no source to refresh).
func TestSetNameWithoutSourceEmitsOnlyRoster(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "Greta")

	out := s.setName("g1", "Margaret")
	if hasFrameOfType(out, "slot-rebind") {
		t.Fatalf("setName on a peer with no source must not emit slot-rebind, got %+v", out)
	}
	if e := entryByID(t, framesToRoster(t, out, "host"), "g1"); e.Name != "Margaret" {
		t.Fatalf("roster after setName has name %q, want Margaret", e.Name)
	}
}

// AC-7: setName is a no-op (no frames) for an unchanged name or an absent peer — so a redundant
// override doesn't churn the roster or flicker a nameplate.
func TestSetNameNoopForUnchangedOrAbsent(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "Greta")

	if out := s.setName("g1", "Greta"); out != nil {
		t.Fatalf("setName to the same name must be a no-op, got %+v", out)
	}
	if out := s.setName("ghost", "Whoever"); out != nil {
		t.Fatalf("setName for an absent peer must be a no-op, got %+v", out)
	}
}

// framesToRoster returns the roster (t:roster) frame addressed to `to`, failing if absent.
func framesToRoster(t *testing.T, out []outbound, to PeerID) []RosterEntry {
	t.Helper()
	f, ok := firstFrameOfType(out, to, "roster")
	if !ok {
		t.Fatalf("no roster frame addressed to %q in %+v", to, out)
	}
	return f.Peers
}
