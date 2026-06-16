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

// codex: {t:screen-roster} is a full-state SNAPSHOT — when the last sharer stops (clearing the pool
// and the live slot), the broadcast carries the empty pool + no live, so the host resets its rail.
func TestScreenRosterClearSnapshot(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	joinSharer(s, "g1")
	s.screenStart("g1")
	s.screenSelect("host", "g1")

	out := s.screenStop("g1") // last sharer + the live one
	f, ok := firstFrameOfType(out, "host", "screen-roster")
	if !ok || len(f.Previews) != 0 || f.Live != "" {
		t.Fatalf("clearing screen-roster = previews:%v live:%q, want an empty snapshot", f.Previews, f.Live)
	}
}

// codex: a HOST joining (or reconnecting) while guests are already sharing must REPLAY the current
// screen-roster, so its preview rail populates immediately — not only after the next start/select.
func TestHostJoinReplaysScreenRoster(t *testing.T) {
	s := newRoomState()
	joinSharer(s, "g1")
	s.screenStart("g1") // sharing before any host socket is present

	out := s.join("host", "host", "")
	f, ok := firstFrameOfType(out, "host", "screen-roster")
	if !ok || len(f.Previews) != 1 || f.Previews[0] != "g1" {
		t.Fatalf("host join must replay the screen-roster, got %+v", f)
	}
	// A host joining a room with nothing shared gets no replay (the rail starts empty).
	s2 := newRoomState()
	if out := s2.join("host", "host", ""); hasFrameOfType(out, "screen-roster") {
		t.Fatal("a host joining with nothing shared must not get a screen-roster replay")
	}
}

// codex: a GENERIC {t:rebind,slot:"screen"} must be REJECTED — the screenshare slot is managed ONLY
// by screen-select over the pool, so a stale/hostile generic rebind can't mark an arbitrary peer the
// live share without pool membership. (Driven through the Room actor: the rejected rebind emits no
// slot-rebind, so the screen source's FIRST slot-rebind names the screen-SELECTed peer.)
func TestRoomGenericRebindRejectsScreenSlot(t *testing.T) {
	r := newRoom("screengate", nil, nil)
	go r.run()
	defer r.Close()

	r.Join("host", "host", "", "", make(chan Frame, 16))
	r.Join("g1", "guest", "", "", make(chan Frame, 16))
	r.Join("g2", "guest", "", "", make(chan Frame, 16))
	r.SetScreenEligible("g1", true)
	r.SetScreenEligible("g2", true)

	srcOut := make(chan Frame, 16)
	r.Join("src-screen", "obs", "", "screen", srcOut)
	if f := recvFrame(t, srcOut); f.T != "slot-unbound" {
		t.Fatalf("screen source initial frame = %q, want slot-unbound", f.T)
	}

	r.ScreenStart("g1")
	r.ScreenStart("g2")
	r.Rebind("screen", "g1")     // GENERIC rebind of the screen slot — must be rejected (no frame, no bind)
	r.ScreenSelect("host", "g2") // the sanctioned path → binds the slot to g2

	if f := recvFrameOfType(t, srcOut, "slot-rebind"); f.OccupantPeerID != "g2" {
		t.Fatalf("screen source first slot-rebind occupant = %q, want g2 — a generic {t:rebind,slot:screen} must not bind g1", f.OccupantPeerID)
	}
}
