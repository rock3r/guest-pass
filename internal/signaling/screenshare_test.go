package signaling

import "testing"

// joinSharer joins an eligible (can_screen) guest so it may enter the screenshare pool.
func joinSharer(s *roomState, id string) {
	s.join(PeerID(id), "guest", "")
	s.peers[PeerID(id)].canScreen = true
}

// AC-11/D-21: screen-start adds an ELIGIBLE participant to the preview pool (its roster screenShare
// pointer = "backstage"), broadcasts the host-only screen-roster, and is a no-op for an ineligible
// or share-locked peer + idempotent for one already sharing.
func TestScreenStartAddsToPool(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	joinSharer(s, "g1")
	s.join("g2", "guest", "") // NOT eligible (no can_screen)

	out := s.screenStart("g1")
	if !s.screenPreviews["g1"] {
		t.Fatal("an eligible guest should join the preview pool")
	}
	if e := entryByID(t, s.rosterFor("g1", "guest"), "g1"); e.ScreenShare != "backstage" {
		t.Fatalf("g1 self screenShare = %q, want backstage", e.ScreenShare)
	}
	f, ok := firstFrameOfType(out, "host", "screen-roster")
	if !ok || len(f.Previews) != 1 || f.Previews[0] != "g1" || f.Live != "" {
		t.Fatalf("host screen-roster = %+v, want previews=[g1] live=\"\"", f)
	}

	if out := s.screenStart("g2"); out != nil || s.screenPreviews["g2"] {
		t.Fatal("an ineligible guest's screen-start must be a no-op")
	}
	// A share-locked eligible guest can't start (server-enforced, EN-7).
	s.setLock("g1", "share", &lockState{applier: "host", floor: rankHost})
	joinSharer(s, "g3")
	s.setLock("g3", "share", &lockState{applier: "host", floor: rankHost})
	if out := s.screenStart("g3"); out != nil || s.screenPreviews["g3"] {
		t.Fatal("a share-locked guest's screen-start must be a no-op")
	}
	// Idempotent: re-starting an already-pooled guest is a no-op.
	delete(s.locks, "g1")
	if out := s.screenStart("g1"); out != nil {
		t.Fatal("re-starting an already-sharing guest must be a no-op")
	}
}

// AC-11/D-21: screen-select is HOST-ONLY — a non-host actor is a no-op; the host promotes a backstage
// sharer to live (the "screen" slot occupant + screenShare="live", visible to everyone); selecting a
// non-pooled peer is a no-op; peer "" clears the slot (no auto-advance).
func TestScreenSelectHostOnly(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	joinSharer(s, "g1")
	joinSharer(s, "g2")
	s.screenStart("g1")
	s.screenStart("g2")

	if out := s.screenSelect("g1", "g2"); out != nil || s.screenLiveID() != "" {
		t.Fatal("a non-host screen-select must be a no-op (host-only, D-21)")
	}

	out := s.screenSelect("host", "g1")
	if s.screenLiveID() != "g1" {
		t.Fatalf("host select: live = %q, want g1", s.screenLiveID())
	}
	if e := entryByID(t, s.rosterFor("g1", "guest"), "g1"); e.ScreenShare != "live" {
		t.Fatal("the selected sharer should see its OWN entry as live")
	}
	if e := entryByID(t, s.rosterFor("g2", "guest"), "g1"); e.ScreenShare != "live" {
		t.Fatal("everyone must see the LIVE share (g2 sees g1 live)")
	}
	if f, _ := firstFrameOfType(out, "host", "screen-roster"); f.Live != "g1" {
		t.Fatalf("screen-roster live = %q, want g1", f.Live)
	}

	// Selecting a peer NOT in the pool is a no-op.
	s.join("g3", "guest", "")
	if out := s.screenSelect("host", "g3"); out != nil || s.screenLiveID() != "g1" {
		t.Fatal("selecting a non-sharer must be a no-op")
	}
	// Clear the slot (no auto-advance to g2).
	s.screenSelect("host", "")
	if s.screenLiveID() != "" {
		t.Fatal("screen-select(\"\") must clear the live slot")
	}
}

// AC-11/D-21: the preview rail is host-only — a non-host sees another participant's "backstage" as
// stripped, but sees a participant's "live" (so all clients render the live share) and its OWN
// backstage.
func TestScreenBackstageHostOnly(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	joinSharer(s, "g1")
	joinSharer(s, "g2")
	s.screenStart("g1")

	if e := entryByID(t, s.rosterFor("g2", "guest"), "g1"); e.ScreenShare != "" {
		t.Fatalf("g2 sees g1 backstage (%q) — the preview rail is host-only", e.ScreenShare)
	}
	if e := entryByID(t, s.rosterFor("g1", "guest"), "g1"); e.ScreenShare != "backstage" {
		t.Fatal("g1 must see its OWN backstage state")
	}
	if e := entryByID(t, s.rosterFor("host", "host"), "g1"); e.ScreenShare != "backstage" {
		t.Fatal("the host sees every sharer's backstage")
	}
	// Once live, g2 DOES see it.
	s.screenSelect("host", "g1")
	if e := entryByID(t, s.rosterFor("g2", "guest"), "g1"); e.ScreenShare != "live" {
		t.Fatal("g2 must see the live share")
	}
}

// AC-11/D-21: stop / force-no-share / leave / eligibility-revoke all PULL a sharer from the pool AND
// clear the live slot if it held it — with NO auto-advance to another pooled sharer.
func TestScreenPullClearsLiveNoAutoAdvance(t *testing.T) {
	cases := []struct {
		name string
		pull func(s *roomState)
	}{
		{"stop", func(s *roomState) { s.screenStop("g1") }},
		{"force-no-share", func(s *roomState) { s.force("host", "g1", "share") }},
		{"leave", func(s *roomState) { s.leave("g1") }},
		{"eligibility-revoke", func(s *roomState) { s.setScreenEligibleLive("g1", false) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newRoomState()
			s.join("host", "host", "")
			joinSharer(s, "g1")
			joinSharer(s, "g2")
			s.screenStart("g1")
			s.screenStart("g2")
			s.screenSelect("host", "g1") // g1 live

			c.pull(s)

			if s.screenPreviews["g1"] {
				t.Fatalf("%s must remove g1 from the pool", c.name)
			}
			if s.screenLiveID() != "" {
				t.Fatalf("%s must clear the live slot (no auto-advance), live = %q", c.name, s.screenLiveID())
			}
			if !s.screenPreviews["g2"] {
				t.Fatalf("%s must NOT auto-advance — g2 stays a backstage preview, never auto-promoted", c.name)
			}
		})
	}
}

// AC-11/D-21: the {t:screen-roster} broadcast is HOST-ONLY (a guest learns the live sharer via the
// screenShare="live" roster fold, not this frame).
func TestScreenRosterHostOnly(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	joinSharer(s, "g1")
	out := s.screenStart("g1")
	if _, ok := firstFrameOfType(out, "g1", "screen-roster"); ok {
		t.Fatal("screen-roster must NOT reach a guest (host-only, D-21)")
	}
	if _, ok := firstFrameOfType(out, "host", "screen-roster"); !ok {
		t.Fatal("screen-roster must reach the host")
	}
}
