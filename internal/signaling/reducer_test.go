package signaling

import (
	"encoding/json"
	"strings"
	"testing"
)

// rosterEntryFor returns peer's entry as projected in the first {t:roster} frame addressed
// to `to`, or false if there is no such roster or entry. On-air, presence and locks fold
// into the roster (D-24/EN-8), so this is how a behavior is asserted post-M3.
func rosterEntryFor(out []outbound, to, peer PeerID) (RosterEntry, bool) {
	f, ok := firstFrameOfType(out, to, "roster")
	if !ok {
		return RosterEntry{}, false
	}
	for _, e := range f.Peers {
		if e.ID == string(peer) {
			return e, true
		}
	}
	return RosterEntry{}, false
}

// onAirSeenBy returns the folded three-state on-air of `peer` in the roster delivered to
// `to` (D-24), or "" if no such roster/entry was emitted.
func onAirSeenBy(out []outbound, to, peer PeerID) string {
	e, _ := rosterEntryFor(out, to, peer)
	return e.OnAir
}

// bptr returns a pointer to b, for building {t:state} presence frames where an absent
// (nil) modality means "leave unchanged" (a meter-only update must not clobber presence).
func bptr(b bool) *bool { return &b }

// fptr returns a pointer to f, for a {t:state} audio-meter value (absent = leave unchanged).
func fptr(f float64) *float64 { return &f }

// EN-3: a rebind bumps the epoch, swaps the occupant, resets on-air to unknown, and tells
// the source page to renegotiate to the new occupant at the new epoch. The occupant's folded
// on-air starts at status-unavailable until a fresh transition (D-24).
func TestRebindBumpsEpochAndResetsOnAir(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")

	out := s.rebindSlot("cam-1", "g1")

	st := s.slots["cam-1"]
	if st.epoch != 1 || st.occupant != "g1" || st.onAir != OnAirUnknown {
		t.Fatalf("slot state = %+v, want epoch 1 / g1 / %s", st, OnAirUnknown)
	}
	if sr, ok := firstFrameOfType(out, "src", "slot-rebind"); !ok || sr.OccupantPeerID != "g1" || epochVal(sr) != 1 {
		t.Fatalf("source should get slot-rebind(g1, epoch 1), got %+v", out)
	}
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("g1's folded on-air after rebind = %q, want %s", got, OnAirUnknown)
	}
}

// AC-1/T-1: a participant's {t:state} folds its cam/mic/screen into the roster — every
// viewer's tile reflects it, and the sender's own (self-marked) entry carries it too.
func TestStateFoldsPresenceIntoRoster(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "Greta")

	out := s.applyState("g1", bptr(false), bptr(true), bptr(false), nil) // mic on, cam + screen off

	e, ok := rosterEntryFor(out, "host", "g1")
	if !ok {
		t.Fatalf("host roster should include g1, got %+v", out)
	}
	if e.Cam || !e.Mic || e.Screen || e.Name != "Greta" {
		t.Fatalf("g1 presence = %+v, want name=Greta cam:false mic:true screen:false", e)
	}
	self, ok := rosterEntryFor(out, "g1", "g1")
	if !ok || !self.Self || !self.Mic {
		t.Fatalf("g1's own entry should be self-marked with mic on, got %+v", self)
	}
}

// AD-13: a {t:state} that changes nothing (e.g. re-asserting the same presence) must NOT
// re-broadcast the roster — continuous meters ride the {t:levels} tick (PR-2), not the roster.
func TestStateNoChangeIsQuiet(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyState("g1", bptr(true), bptr(true), bptr(false), nil)
	if out := s.applyState("g1", bptr(true), bptr(true), bptr(false), nil); out != nil {
		t.Fatalf("an unchanged state must not churn the roster, got %+v", out)
	}
}

// A documented meter-only update ({t:state,level} with no cam/mic/screen, modeled here as an
// all-nil presence frame) must NOT clobber presence to off — absent modalities are left
// unchanged, and a frame that changes no modality emits nothing (Codex PR-20).
func TestStateLevelOnlyPreservesPresence(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyState("g1", bptr(true), bptr(true), bptr(true), nil) // all on

	out := s.applyState("g1", nil, nil, nil, fptr(0.6)) // a meter-only {t:state}: level, no presence
	if out != nil {
		t.Fatalf("a presence-less {t:state} must not re-broadcast the roster, got %+v", out)
	}
	if s.peers["g1"].level != 0.6 {
		t.Fatalf("the meter-only update must still store the level, got %v", s.peers["g1"].level)
	}
	// A subsequent change confirms presence was preserved (still all-on, so a partial off shows).
	out = s.applyState("g1", bptr(false), nil, nil, nil) // turn cam off only
	e, ok := rosterEntryFor(out, "host", "g1")
	if !ok || e.Cam || !e.Mic || !e.Screen {
		t.Fatalf("presence must survive a meter-only update; got %+v (want cam:false mic:true screen:true)", e)
	}
}

// EN-7: an OBS source virtual peer has no self-presence; a {t:state} from it is ignored.
func TestStateFromNonParticipantIgnored(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	if out := s.applyState("src", bptr(true), bptr(true), bptr(true), fptr(0.9)); out != nil {
		t.Fatalf("an OBS source has no presence; state must be ignored, got %+v", out)
	}
}

// D-24: a source's streamingStarted/Stopped reflection broadcasts a GLOBAL "we're live"
// state to every participant (host/co-host/guest) — but never to OBS source virtual peers
// (EN-13) — and is not epoch-scoped. This stays a room-level broadcast (not folded into the
// per-guest roster, which carries the per-slot onAir).
func TestObsStreamingBroadcastsToParticipantsOnly(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("src-cam-1", "obs", "") // a source page — must NOT receive the streaming broadcast

	out := s.obsStreaming(true)
	if !s.streaming {
		t.Fatalf("obsStreaming(true) must set the room streaming state")
	}
	got := map[PeerID]bool{}
	for _, o := range out {
		if o.frame.T != "streaming" || !o.frame.Active {
			t.Fatalf("unexpected outbound %+v, want {t:streaming, active:true}", o.frame)
		}
		got[o.to] = true
	}
	if !got["host"] || !got["g1"] {
		t.Fatalf("host and g1 must receive the streaming broadcast, got %v", got)
	}
	if got["src-cam-1"] {
		t.Fatalf("OBS source pages must NOT receive the streaming broadcast (EN-13), got %v", got)
	}

	// streamingStopped flips the global state back and carries active=false.
	out = s.obsStreaming(false)
	if s.streaming {
		t.Fatalf("obsStreaming(false) must clear the room streaming state")
	}
	for _, o := range out {
		if o.frame.Active {
			t.Fatalf("a streamingStopped broadcast must carry active=false, got %+v", o.frame)
		}
	}
}

// D-24: when the OBS source for a slot disconnects, its on-air reflection is gone — the
// occupant's folded on-air must degrade to status-unavailable, never keep asserting a stale
// on-air with no live OBS signal behind it ("never assert when unknown").
func TestSourceLeaveResetsOnAirToUnavailable(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")         // epoch 1, occupant g1
	s.obsSourceActive("cam-1", true, 1) // slot reflects on-air
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("precondition: slot should be on-air, got %q", s.slots["cam-1"].onAir)
	}

	out := s.leave("src", false) // the OBS source disconnects

	if st := s.slots["cam-1"]; st.onAir != OnAirUnknown {
		t.Fatalf("on-air must degrade to %s when the source leaves, got %q", OnAirUnknown, st.onAir)
	}
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("occupant g1's roster must show on-air degraded to %s, got %q (out=%+v)", OnAirUnknown, got, out)
	}
}

// D-38: when an OBS source departs from an OCCUPIED slot, the bound occupant (the guest whose
// publisher served that source) gets a {t:consumer-left} so its connectivity watchdog untracks the
// never-connected source pc — sources are non-participants, hidden from guests by visibleTo
// (roster.go), so a guest gets NO peer-left for one. The notice carries only the source peer id the
// guest already answered (no token) and goes ONLY to the occupant — not other guests or the host.
func TestSourceLeaveNotifiesOccupantConsumerLeft(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")
	s.rebindSlot("cam-1", "g1") // g1 occupies cam-1, sourced by "src"

	out := s.leave("src", false) // the OBS source disconnects

	if cl, ok := firstFrameOfType(out, "g1", "consumer-left"); !ok || cl.PeerID != "src" {
		t.Fatalf("occupant g1 must get consumer-left(src), got %+v", framesTo(out, "g1"))
	}
	if _, ok := firstFrameOfType(out, "g2", "consumer-left"); ok {
		t.Fatalf("a non-occupant guest must NOT get consumer-left, got %+v", framesTo(out, "g2"))
	}
	if _, ok := firstFrameOfType(out, "host", "consumer-left"); ok {
		t.Fatalf("the host must NOT get consumer-left, got %+v", framesTo(out, "host"))
	}
}

// D-38: a source leaving an UNOCCUPIED slot notifies no one (pins the st.occupant != "" guard — there
// is no guest publisher tracking that source pc, so there is nothing to untrack).
func TestSourceLeaveUnoccupiedSlotNotifiesNoOne(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src") // attached, but the slot has no occupant

	out := s.leave("src", false)

	for _, to := range []PeerID{"g1", "host"} {
		if _, ok := firstFrameOfType(out, to, "consumer-left"); ok {
			t.Fatalf("no consumer-left expected (unoccupied slot), but %s got one: %+v", to, framesTo(out, to))
		}
	}
}

// D-38: rebinding a sourced slot AWAY from its occupant leaves the PRIOR occupant's publisher with a
// (possibly never-connected) pc to that source, which the prior occupant gets no peer-left for. So a
// rebind notifies the prior occupant with consumer-left(source) — but NOT the new occupant (which is
// about to receive a fresh offer from the same source).
func TestSlotRebindNotifiesPriorOccupantConsumerLeft(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("a", "guest", "")
	s.join("b", "guest", "")
	s.rebindSlot("cam-1", "a") // a occupies, sourced by "src"

	out := s.rebindSlot("cam-1", "b") // rebind away to b → a's source pc is now stale

	if cl, ok := firstFrameOfType(out, "a", "consumer-left"); !ok || cl.PeerID != "src" {
		t.Fatalf("prior occupant a must get consumer-left(src) on rebind-away, got %+v", framesTo(out, "a"))
	}
	if _, ok := firstFrameOfType(out, "b", "consumer-left"); ok {
		t.Fatalf("the new occupant b must NOT get consumer-left, got %+v", framesTo(out, "b"))
	}
}

// D-38: unbinding a sourced slot leaves the occupant's publisher with a stale source pc → notify it.
func TestSlotUnbindNotifiesOccupantConsumerLeft(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("a", "guest", "")
	s.rebindSlot("cam-1", "a")

	out := s.unbindSlot("cam-1") // host unbinds → a's source pc is now stale

	if cl, ok := firstFrameOfType(out, "a", "consumer-left"); !ok || cl.PeerID != "src" {
		t.Fatalf("occupant a must get consumer-left(src) on unbind, got %+v", framesTo(out, "a"))
	}
}

// D-38: a re-bind to the SAME occupant (e.g. a reconnect re-bind) is not a consumer change — the
// occupant still consumes the source, so it must NOT get a spurious consumer-left.
func TestSlotRebindSameOccupantNoConsumerLeft(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("a", "guest", "")
	s.rebindSlot("cam-1", "a")

	out := s.rebindSlot("cam-1", "a") // re-bind to the same occupant

	if _, ok := firstFrameOfType(out, "a", "consumer-left"); ok {
		t.Fatalf("a re-bind to the SAME occupant must not emit consumer-left, got %+v", framesTo(out, "a"))
	}
}

// D-38: when the occupant itself LEAVES the room, its slot is vacated — but it is already gone, so no
// (pointless) consumer-left is sent to the departed peer (the guard skips a prior occupant no longer
// in the room; its publisher is closing anyway).
func TestOccupantLeaveDoesNotNotifyDepartedOccupant(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("a", "guest", "")
	s.rebindSlot("cam-1", "a")

	out := s.leave("a", false) // the occupant disconnects

	if _, ok := firstFrameOfType(out, "a", "consumer-left"); ok {
		t.Fatalf("a departed occupant must not be sent consumer-left, got %+v", framesTo(out, "a"))
	}
}

// D-38: a source's signals are relayed ONLY to its slot's CURRENT occupant. A stale source offer to a
// PRIOR occupant (after a rebind/unbind) is dropped — so the prior occupant, which just dropped that
// source's pc on {t:consumer-left}, can't recreate it from a late offer and re-arm the watchdog. A
// non-source (mesh) relay is unaffected.
func TestRelaySignalFromSourceOnlyReachesCurrentOccupant(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("a", "guest", "")
	s.join("b", "guest", "")
	s.rebindSlot("cam-1", "a")

	if out := s.relaySignal("src", Frame{To: "a"}); len(out) != 1 || out[0].to != "a" {
		t.Fatalf("a source signal to its current occupant must relay, got %+v", out)
	}

	s.rebindSlot("cam-1", "b") // rebind away from a
	if out := s.relaySignal("src", Frame{To: "a"}); len(out) != 0 {
		t.Fatalf("a stale source signal to the prior occupant must be dropped, got %+v", out)
	}
	if out := s.relaySignal("src", Frame{To: "b"}); len(out) != 1 || out[0].to != "b" {
		t.Fatalf("a source signal to the new occupant must relay, got %+v", out)
	}
	if out := s.relaySignal("a", Frame{To: "b"}); len(out) != 1 || out[0].to != "b" {
		t.Fatalf("a non-source (mesh) relay must be unaffected by the source gate, got %+v", out)
	}
}

// D-21 screen media path: relaySignal must PRESERVE the channel discriminator (Ch), so a peer pair's
// SECOND P2P connection (the screenshare track, ch="screen") is distinguishable from the camera
// (ch="") at both ends. The clean outbound frame still carries only {t,from,sdp,ice,ch}.
func TestRelaySignalPreservesChannel(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	joinSharer(s, "a")
	s.screenStart("a")
	s.screenSelect("host", "a") // a is the live sharer → its screen is consumable by anyone

	out := s.relaySignal("host", Frame{To: "a", Ch: "screen", SDP: []byte(`"x"`)})
	if len(out) != 1 || out[0].to != "a" {
		t.Fatalf("a screen-channel signal must relay to a, got %+v", out)
	}
	if out[0].frame.Ch != "screen" {
		t.Fatalf("relaySignal must preserve the channel discriminator, got Ch=%q", out[0].frame.Ch)
	}
	if out[0].frame.From != "host" {
		t.Fatalf("from must be stamped, got From=%q", out[0].frame.From)
	}

	// A camera signal (no channel) relays with an empty Ch, unchanged.
	cam := s.relaySignal("host", Frame{To: "a", SDP: []byte(`"x"`)})
	if len(cam) != 1 || cam[0].frame.Ch != "" {
		t.Fatalf("a camera signal must carry no channel, got %+v", cam)
	}
}

// D-21/EN-7 screen-channel authorization: a backstage (non-live) sharer's screen is consumable ONLY
// by the host (the host-only preview rail, EN-8); the live sharer's screen is consumable by anyone
// (the live render). A non-host can't pull a non-live sharer's screen by crafting a ch="screen" offer.
func TestRelaySignalScreenChannelAuthorization(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	joinSharer(s, "a")
	joinSharer(s, "b")
	s.join("g", "guest", "")
	s.screenStart("a") // a backstage (not live)
	s.screenStart("b") // b backstage (not live)

	scr := func(from, to string) []outbound {
		return s.relaySignal(PeerID(from), Frame{To: to, Ch: "screen", SDP: []byte(`"x"`)})
	}

	if out := scr("host", "a"); len(out) != 1 {
		t.Fatalf("host may consume a backstage sharer's screen (the rail), got %+v", out)
	}
	if out := scr("g", "a"); len(out) != 0 {
		t.Fatalf("a non-host must NOT consume a backstage sharer's screen, got %+v", out)
	}

	s.screenSelect("host", "a") // promote a live (b stays a backstage sharer)
	if out := scr("g", "a"); len(out) != 1 {
		t.Fatalf("anyone may consume the LIVE sharer's screen, got %+v", out)
	}
	if out := scr("a", "g"); len(out) != 1 {
		t.Fatalf("the live sharer's answer back to a consumer must relay, got %+v", out)
	}
	// A backstage sharer (b) renders the LIVE share (a) — both ends are sharers, but the live one is
	// public, so the connection (and its answer) is authorized.
	if out := scr("b", "a"); len(out) != 1 {
		t.Fatalf("a backstage sharer may consume the LIVE sharer's screen, got %+v", out)
	}
	if out := scr("a", "b"); len(out) != 1 {
		t.Fatalf("the live sharer's answer to a backstage-sharer consumer must relay, got %+v", out)
	}
	if out := scr("g", "b"); len(out) != 0 {
		t.Fatalf("a non-host still must NOT consume a different backstage sharer (b), got %+v", out)
	}
	// A screen signal between two non-sharers is rejected (no legitimate screen link).
	if out := scr("g", "host"); len(out) != 0 {
		t.Fatalf("a screen signal between two non-sharers must be dropped, got %+v", out)
	}
	// The camera channel is unaffected by the screen gate.
	if out := s.relaySignal("g", Frame{To: "a", SDP: []byte(`"x"`)}); len(out) != 1 {
		t.Fatalf("a camera signal must be unaffected by the screen gate, got %+v", out)
	}
}

// D-21/EN-7: an OBS source is locked to ONE channel by its authenticated slot. The screenshare-slot
// source (obs_screen) may negotiate ONLY the screen channel with its occupant; a cam/host source ONLY
// the camera channel — so a screenshare source token can't pull its occupant's camera and vice versa.
func TestRelaySignalSourceChannelBoundToSlot(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	joinSharer(s, "a")
	s.screenStart("a")
	s.screenSelect("host", "a") // a is the live screen occupant

	s.join("srcscreen", "obs_screen", "")
	s.attachSource("screen", "srcscreen")
	s.join("srccam", "obs", "")
	s.rebindSlot("cam-1", "a")
	s.attachSource("cam-1", "srccam")

	scr := func(from, to, ch string) []outbound {
		return s.relaySignal(PeerID(from), Frame{To: to, Ch: ch, SDP: []byte(`"x"`)})
	}

	if out := scr("srcscreen", "a", "screen"); len(out) != 1 {
		t.Fatalf("the screen source on its screen channel must relay, got %+v", out)
	}
	if out := scr("srcscreen", "a", ""); len(out) != 0 {
		t.Fatalf("the screen source must NOT pull the occupant's CAMERA (ch=\"\"), got %+v", out)
	}
	if out := scr("srccam", "a", ""); len(out) != 1 {
		t.Fatalf("the cam source on the camera channel must relay, got %+v", out)
	}
	if out := scr("srccam", "a", "screen"); len(out) != 0 {
		t.Fatalf("the cam source must NOT request the screen channel, got %+v", out)
	}
}

// EN-3 (the keystone): after a rebind, a STALE obsSourceActive carrying the previous epoch
// must NOT light the new occupant; only the current epoch's event applies.
func TestStaleObsActiveIgnoredAfterRebind(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1") // epoch 1

	out := s.obsSourceActive("cam-1", true, 1)
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("epoch-1 active should light the slot, got %q", s.slots["cam-1"].onAir)
	}
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirYes {
		t.Fatalf("g1's roster should show on-air, got %q", got)
	}

	s.join("g2", "guest", "")
	s.rebindSlot("cam-1", "g2") // epoch 2, on-air reset
	if s.slots["cam-1"].onAir != OnAirUnknown {
		t.Fatalf("rebind must reset on-air to %s", OnAirUnknown)
	}

	// stale epoch-1 active from the old occupant: ignored, emits nothing.
	if out := s.obsSourceActive("cam-1", true, 1); out != nil {
		t.Fatalf("stale epoch-1 active must be ignored, got %+v", out)
	}
	if s.slots["cam-1"].onAir != OnAirUnknown {
		t.Fatalf("stale active must NOT light the new occupant (EN-3)")
	}

	// current epoch lights g2.
	s.obsSourceActive("cam-1", true, 2)
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("epoch-2 active should light g2")
	}
}

// A source that attaches after a slot is already bound learns the current binding.
func TestAttachSourceGetsCurrentBinding(t *testing.T) {
	s := newRoomState()
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1") // bound before any source attaches

	s.join("src", "obs", "")
	out := s.attachSource("cam-1", "src")
	if sr, ok := firstFrameOfType(out, "src", "slot-rebind"); !ok || sr.OccupantPeerID != "g1" {
		t.Fatalf("attach should deliver the current binding to the source, got %+v", out)
	}
}

// A rebind naming a peer that isn't in the room is a no-op: it must not advance the
// epoch or bind the slot to a peer that can't receive media/on-air.
func TestRebindToUnknownPeerIsNoOp(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")

	if out := s.rebindSlot("cam-1", "ghost"); out != nil {
		t.Fatalf("rebind to unknown peer should emit nothing, got %+v", out)
	}
	if st := s.slots["cam-1"]; st.epoch != 0 || st.occupant != "" {
		t.Fatalf("rebind to unknown peer must not mutate the slot, got %+v", st)
	}
}

func TestUnbindBumpsEpochAndPlaceholders(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")

	out := s.unbindSlot("cam-1")
	st := s.slots["cam-1"]
	if st.epoch != 2 || st.occupant != "" || st.onAir != OnAirUnknown {
		t.Fatalf("unbind state = %+v", st)
	}
	if su, ok := firstFrameOfType(out, "src", "slot-unbound"); !ok || epochVal(su) != 2 {
		t.Fatalf("source should get slot-unbound(epoch 2), got %+v", out)
	}
}

// D-24: unbinding a slot that WAS on-air degrades the occupant's folded on-air to
// status-unavailable (it is no longer sourced), alongside the slot-unbound to the source.
func TestUnbindDegradesOnAirOccupant(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, 1) // g1 on-air

	out := s.unbindSlot("cam-1")
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("unbinding an on-air slot must degrade the occupant's pill (D-24), got %q (out=%+v)", got, out)
	}
}

// D-24: reassigning a slot from occupant A to B must degrade A's pill — A is no longer
// sourced here, so it can't keep asserting the on-air it last had (later sourceActive frames
// go to B). The new occupant B's pill stays at its default until a fresh transition.
func TestRebindDegradesDisplacedOccupant(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("a", "guest", "")
	s.join("b", "guest", "")
	s.rebindSlot("cam-1", "a")
	s.obsSourceActive("cam-1", true, 1) // a is on-air

	out := s.rebindSlot("cam-1", "b") // reassign the slot to b

	if got := onAirSeenBy(out, "a", "a"); got != OnAirUnknown {
		t.Fatalf("displaced occupant a must degrade to %s, got %q", OnAirUnknown, got)
	}
	// The incoming occupant b must NEVER inherit the prior occupant's on-air (EN-3).
	if got := onAirSeenBy(out, "b", "b"); got != OnAirUnknown {
		t.Fatalf("the new occupant must not inherit a stale on-air, got %q", got)
	}
}

// D-24: re-applying a slot to the SAME occupant (a host bumping the epoch to renegotiate)
// still degrades that occupant — the slot's on-air is stale at the new epoch until a fresh
// transition, so the guest must not keep showing the prior pill.
func TestRebindSameOccupantStillDegrades(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, 1) // g1 on-air

	out := s.rebindSlot("cam-1", "g1") // re-apply to the same occupant

	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("re-applying a slot to the same on-air occupant must degrade its pill (D-24), got %q", got)
	}
}

// D-24: a source reconnect/reload that re-attaches (Room.Join evicts the old conn WITHOUT
// running leave) must reset a stale on-air and degrade the occupant — the refreshed source
// has reported no transition yet, so the state is UNKNOWN.
func TestSourceReattachResetsStaleOnAir(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, 1) // slot is on-air
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("precondition: slot should be on-air")
	}

	// The source page refreshes: the replacement connection re-attaches to the same slot.
	out := s.attachSource("cam-1", "src")

	if st := s.slots["cam-1"]; st.onAir != OnAirUnknown {
		t.Fatalf("a re-attaching source must reset stale on-air to %s, got %q", OnAirUnknown, st.onAir)
	}
	if got := onAirSeenBy(out, "g1", "g1"); got != OnAirUnknown {
		t.Fatalf("a re-attaching source must degrade the occupant's pill (D-24), got %q (out=%+v)", got, out)
	}
}

// D-24: a participant joining/rejoining mid-stream gets the room's current OBS reflections —
// the global "we're live" state (room-level {t:streaming}), and the on-air of any slot it
// already occupies folded into its fresh roster (an eviction-rejoin keeps its binding) — so
// it doesn't sit at the defaults until OBS next toggles. OBS source virtual peers get neither
// (EN-13). The interim per-slot {t:onair} replay is retired.
func TestJoinReplaysCurrentObsState(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, 1) // slot on-air
	s.obsStreaming(true)                // broadcast live

	// A new participant joining mid-stream is told the broadcast is live.
	out := s.join("h", "host", "")
	var hStreaming bool
	for _, o := range out {
		if o.to == "h" && o.frame.T == "streaming" && o.frame.Active {
			hStreaming = true
		}
	}
	if !hStreaming {
		t.Fatalf("a participant joining mid-stream must be replayed the live state, got %+v", out)
	}

	// An OBS source page joining is NOT replayed participant reflections (EN-13).
	out = s.join("src2", "obs", "")
	for _, o := range out {
		if o.frame.T == "streaming" || o.frame.T == "roster" || o.frame.T == "onair" {
			t.Fatalf("OBS source pages must not receive streaming/roster replays, got %+v", o.frame)
		}
	}

	// The occupant rejoining (eviction keeps its binding) gets its slot's on-air folded into the
	// fresh roster — its self entry shows on-air, no separate {t:onair} frame.
	out = s.join("g1", "guest", "")
	for _, o := range framesTo(out, "g1") {
		if o.T == "onair" {
			t.Fatalf("the interim {t:onair} frame must be retired; on-air rides the roster, got %+v", o)
		}
	}
	e, ok := rosterEntryFor(out, "g1", "g1")
	if !ok || !e.Self || e.OnAir != OnAirYes {
		t.Fatalf("a rejoining occupant's roster self entry must show on-air, got %+v (out=%+v)", e, out)
	}
}

// D-24/EN-3: a source reattach (eviction reload — Room.Join swaps the conn without running
// leave) bumps the epoch, so an in-flight sourceActive from the EVICTED connection — carrying
// the old epoch — is rejected and can't re-light a stale pill after the reset.
func TestReattachBumpsEpochInvalidatingStaleReports(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")         // epoch 1
	s.obsSourceActive("cam-1", true, 1) // on-air at epoch 1
	oldEpoch := s.slots["cam-1"].epoch

	s.attachSource("cam-1", "src") // the source page reloads → reattach (same peer id)
	if s.slots["cam-1"].epoch == oldEpoch {
		t.Fatalf("a source reattach must bump the epoch to invalidate stale reports, still %d", oldEpoch)
	}

	// A stale sourceActive from the evicted connection (old epoch) must be ignored, leaving the
	// pill at the status-unavailable the reattach reset it to.
	if out := s.obsSourceActive("cam-1", true, oldEpoch); out != nil {
		t.Fatalf("a stale sourceActive at the old epoch must be ignored after reattach, got %+v", out)
	}
	if s.slots["cam-1"].onAir != OnAirUnknown {
		t.Fatalf("a stale report must NOT re-light the pill after reattach; on-air = %q", s.slots["cam-1"].onAir)
	}
}

func TestRelaySignalToKnownPeerOnly(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest", "")
	s.join("b", "guest", "")

	out := s.relaySignal("a", Frame{T: "signal", To: "b", SDP: []byte(`{"x":1}`)})
	if len(out) != 1 || out[0].to != "b" || out[0].frame.From != "a" || out[0].frame.To != "" {
		t.Fatalf("relay = %+v, want one frame to b stamped from=a", out)
	}
	if got := s.relaySignal("a", Frame{T: "signal", To: "ghost"}); got != nil {
		t.Fatalf("relay to unknown peer must drop, got %+v", got)
	}

	s.leave("b", false)
	if got := s.relaySignal("a", Frame{T: "signal", To: "b"}); got != nil {
		t.Fatalf("relay to a departed peer must drop, got %+v", got)
	}
}

// The relayed frame carries ONLY the opaque payload (sdp/ice) stamped with the sender —
// a peer must not be able to inject roster/slot/control fields into a frame the addressee
// will act on (D-23: the server relays SDP/ICE, nothing else).
func TestRelaySignalStripsExtraneousFields(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest", "")
	s.join("b", "guest", "")

	sdp := []byte(`{"type":"offer","sdp":"v=0..."}`)
	in := Frame{
		T: "signal", To: "b", SDP: sdp,
		// Hostile extras a client must not be able to smuggle through the relay:
		Slot: "cam-1", Epoch: epochPtr(9), Reason: "kicked", OnAir: "on-air",
		OccupantPeerID: "x", Event: "sourceActive", Active: true,
		Cam: bptr(true), Mic: bptr(true), Screen: bptr(true),
		Peers:  []RosterEntry{{ID: "fake", Role: "host"}},
		Peer:   &RosterEntry{ID: "fake", Role: "host"},
		PeerID: "fake",
	}
	out := s.relaySignal("a", in)
	if len(out) != 1 || out[0].to != "b" {
		t.Fatalf("relay = %+v, want one frame to b", out)
	}
	got := out[0].frame
	if got.T != "signal" || got.From != "a" || string(got.SDP) != string(sdp) {
		t.Fatalf("relayed core = %+v, want signal/from=a/same sdp", got)
	}
	if got.To != "" || got.Slot != "" || got.Epoch != nil || got.Reason != "" || got.OnAir != "" ||
		got.OccupantPeerID != "" || got.Event != "" || got.Active ||
		got.Cam != nil || got.Mic != nil || got.Screen != nil ||
		got.Peers != nil || got.Peer != nil || got.PeerID != "" {
		t.Fatalf("relayed frame leaked client-supplied fields: %+v", got)
	}
}

// epochVal dereferences a frame's epoch for assertions, returning -1 when absent (so a
// missing epoch never accidentally equals a real one).
func epochVal(f Frame) int {
	if f.Epoch == nil {
		return -1
	}
	return *f.Epoch
}

// On the wire, epoch rides ONLY slot frames: a relayed signal omits it entirely, while a
// slot-unbound carries it even at epoch 0 (EN-3).
func TestEpochOnlySerializesForSlotFrames(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest", "")
	s.join("b", "guest", "")

	relayed := s.relaySignal("a", Frame{T: "signal", To: "b", SDP: []byte(`"x"`)})
	if b, _ := json.Marshal(relayed[0].frame); strings.Contains(string(b), "epoch") {
		t.Fatalf("a relayed signal must not carry epoch on the wire, got %s", b)
	}

	s.join("src", "obs", "")
	out := s.attachSource("cam-1", "src") // fresh slot → slot-unbound at epoch 0
	b, _ := json.Marshal(out[0].frame)
	if !strings.Contains(string(b), `"epoch":0`) {
		t.Fatalf("a slot frame must carry epoch even at 0, got %s", b)
	}
}

// SDP and ICE are opaque (json.RawMessage): the server relays them byte-for-byte and
// never parses them (D-23). An ICE-only frame relays just as an SDP one does.
func TestRelaySignalRelaysPayloadVerbatim(t *testing.T) {
	s := newRoomState()
	s.join("a", "guest", "")
	s.join("b", "guest", "")

	// An ICE candidate the server has no schema for — relayed unchanged.
	ice := []byte(`{"candidate":"candidate:1 1 udp 2122260223 192.0.2.1 54321 typ host","sdpMid":"0","sdpMLineIndex":0}`)
	out := s.relaySignal("a", Frame{T: "signal", To: "b", ICE: ice})
	if len(out) != 1 || string(out[0].frame.ICE) != string(ice) || out[0].frame.From != "a" {
		t.Fatalf("ICE relay = %+v, want byte-identical ice stamped from=a", out)
	}
	// A payload that isn't even a JSON object (the server never inspects shape).
	weird := []byte(`"just-a-string"`)
	out = s.relaySignal("a", Frame{T: "signal", To: "b", SDP: weird})
	if len(out) != 1 || string(out[0].frame.SDP) != string(weird) {
		t.Fatalf("opaque sdp relay = %+v, want byte-identical %s", out, weird)
	}
}

// hasFrameType reports whether any outbound carries frame type t.
func hasFrameType(out []outbound, t string) bool {
	for _, o := range out {
		if o.frame.T == t {
			return true
		}
	}
	return false
}

// A cam-slot occupant's transient drop RETAINS the binding for the grace window (D-40/AC-3): the
// slot keeps its occupant, no slot-unbound reaches the source (no placeholder flash), and the epoch
// is unchanged. The grace state is recorded for the effectful room's deferred expiry.
func TestLeaveRetainsCamSlotBindingForGrace(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	epochBefore := s.slots["cam-1"].epoch

	out := s.leave("g1", false)

	st := s.slots["cam-1"]
	if st.occupant != "g1" {
		t.Fatalf("a transient drop must retain the binding, got occupant=%q", st.occupant)
	}
	if !st.disconnected || st.graceGen != 1 {
		t.Fatalf("the slot should be grace-pending (disconnected=true, graceGen=1), got %+v", st)
	}
	if st.epoch != epochBefore {
		t.Fatalf("a grace-retained drop must not bump the epoch (got %d, want %d)", st.epoch, epochBefore)
	}
	if hasFrameType(out, "slot-unbound") {
		t.Fatalf("a transient drop must NOT send slot-unbound (no placeholder flash), got %+v", out)
	}
}

// Entering grace degrades the slot's stale on-air to status-unavailable (the OBS link is dead, so
// the prior reflection is no longer truthful — D-24), but WITHOUT slot-unbound or an epoch bump, so
// a reconnect's join roster (before ResumeBind) can't briefly assert on-air off a dead link.
func TestLeaveDegradesStaleOnAirWhenEnteringGrace(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.obsSourceActive("cam-1", true, s.slots["cam-1"].epoch) // the slot reports on-air
	if s.slots["cam-1"].onAir != OnAirYes {
		t.Fatalf("precondition: slot should be on-air, got %q", s.slots["cam-1"].onAir)
	}
	epochBefore := s.slots["cam-1"].epoch

	out := s.leave("g1", false) // transient drop → grace

	st := s.slots["cam-1"]
	if st.occupant != "g1" || !st.disconnected {
		t.Fatalf("grace should retain the binding, got %+v", st)
	}
	if st.onAir != OnAirUnknown {
		t.Fatalf("entering grace must degrade the stale on-air to %s, got %q", OnAirUnknown, st.onAir)
	}
	if st.epoch != epochBefore {
		t.Fatalf("grace must not bump the epoch (got %d, want %d)", st.epoch, epochBefore)
	}
	if hasFrameType(out, "slot-unbound") {
		t.Fatalf("grace must not send slot-unbound, got %+v", out)
	}
}

// An OBS sourceActive echo arriving WHILE the slot is in its grace window (epoch unchanged, but the
// occupant's media is dead) must NOT re-assert on-air — that would undo the entering-grace degrade
// and mislight a dead link (D-24). The rejoin's epoch bump lets a real transition re-assert later.
func TestObsSourceActiveIgnoredDuringGrace(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.leave("g1", false) // grace → on-air degraded to status-unavailable
	if s.slots["cam-1"].onAir != OnAirUnknown {
		t.Fatalf("precondition: on-air degraded on entering grace, got %q", s.slots["cam-1"].onAir)
	}

	out := s.obsSourceActive("cam-1", true, s.slots["cam-1"].epoch) // an echo at the unchanged epoch
	if s.slots["cam-1"].onAir != OnAirUnknown {
		t.Fatalf("a sourceActive echo during grace must be ignored, got %q", s.slots["cam-1"].onAir)
	}
	if out != nil {
		t.Fatalf("an ignored echo should emit nothing, got %+v", out)
	}
}

// After the grace window with no rejoin, expireGrace vacates the slot — exactly today's behavior,
// just deferred: occupant cleared, epoch bumped, slot-unbound to the source (placeholder).
func TestExpireGraceVacatesAfterWindow(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	epochBefore := s.slots["cam-1"].epoch
	s.leave("g1", false)

	out := s.expireGrace("cam-1", "g1", s.slots["cam-1"].graceGen)

	st := s.slots["cam-1"]
	if st.occupant != "" || st.disconnected || st.epoch != epochBefore+1 {
		t.Fatalf("expireGrace should vacate (occupant cleared, epoch+1), got %+v", st)
	}
	if !hasFrameType(out, "slot-unbound") {
		t.Fatalf("expireGrace should emit slot-unbound to the source, got %+v", out)
	}
}

// A rejoin within the grace window re-binds the slot (resumeBind → slot-rebind, new epoch) and
// clears the grace state, so the later expireGrace for the original disconnect is a no-op.
func TestRejoinWithinGraceResumesAndDefusesExpiry(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.leave("g1", false)
	gen := s.slots["cam-1"].graceGen

	// The guest reconnects (re-registers) and the /ws join replays its persisted binding.
	s.join("g1", "guest", "")
	out := s.resumeBind("cam-1", "g1")

	st := s.slots["cam-1"]
	if st.occupant != "g1" || st.disconnected {
		t.Fatalf("rejoin should resume the binding and clear grace, got %+v", st)
	}
	if !hasFrameType(out, "slot-rebind") {
		t.Fatalf("rejoin should re-send slot-rebind so the source re-links, got %+v", out)
	}
	// The original disconnect's expiry must now no-op (the guest is back).
	if exp := s.expireGrace("cam-1", "g1", gen); exp != nil {
		t.Fatalf("expireGrace must no-op after a rejoin, got %+v", exp)
	}
	if s.slots["cam-1"].occupant != "g1" {
		t.Fatal("a stale expiry must not vacate a resumed slot")
	}
}

// A rejoin-then-redisconnect within the original window arms a NEW grace (graceGen advances); the
// FIRST disconnect's expiry no-ops, and only the second disconnect's expiry can vacate.
func TestGraceGenerationGuardsAgainstStaleExpiry(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.leave("g1", false)
	gen1 := s.slots["cam-1"].graceGen

	s.join("g1", "guest", "")
	s.resumeBind("cam-1", "g1")
	s.leave("g1", false) // drops AGAIN within the original window
	gen2 := s.slots["cam-1"].graceGen
	if gen2 == gen1 {
		t.Fatal("a second disconnect must advance graceGen")
	}

	// The first disconnect's stale expiry must not vacate the second grace.
	if exp := s.expireGrace("cam-1", "g1", gen1); exp != nil {
		t.Fatalf("a stale (gen1) expiry must no-op, got %+v", exp)
	}
	if s.slots["cam-1"].occupant != "g1" {
		t.Fatal("the stale expiry vacated a still-grace-pending slot")
	}
	// The current disconnect's expiry vacates.
	if exp := s.expireGrace("cam-1", "g1", gen2); !hasFrameType(exp, "slot-unbound") {
		t.Fatalf("the current (gen2) expiry should vacate, got %+v", exp)
	}
}

// A reconnect that lands in the narrow window between Room.Join (re-adds the peer) and the
// ResumeBind that clears `disconnected` must not be vacated by a grace timer firing there — the
// in-flight resume re-affirms the binding (no placeholder flash on a boundary-time rejoin, AC-3).
func TestExpireGraceNoOpWhenOccupantReconnected(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.leave("g1", false)
	gen := s.slots["cam-1"].graceGen

	// The guest reconnects (Join re-adds it to s.peers) but ResumeBind hasn't run yet.
	s.join("g1", "guest", "")
	if !s.slots["cam-1"].disconnected {
		t.Fatal("precondition: disconnected stays set until ResumeBind clears it")
	}
	if out := s.expireGrace("cam-1", "g1", gen); out != nil {
		t.Fatalf("expireGrace must no-op when the occupant has reconnected, got %+v", out)
	}
	if s.slots["cam-1"].occupant != "g1" {
		t.Fatal("a reconnected occupant's slot must not be vacated by a boundary-time grace timer")
	}
}

// A TERMINAL leave (an eviction) of a guest that is CURRENTLY in its grace window — disconnected,
// no longer in s.peers — must still vacate its grace-bound slot immediately, not leave a zombie
// binding alive until the grace expires. This backs the EvictPeers c==nil path.
func TestTerminalLeaveVacatesGraceBoundSlotWhenDisconnected(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.leave("g1", false) // transient → grace-bound (g1 gone from peers, slot retained)
	if s.slots["cam-1"].occupant != "g1" || !s.slots["cam-1"].disconnected {
		t.Fatalf("precondition: grace-bound, got %+v", s.slots["cam-1"])
	}

	out := s.leave("g1", true) // a terminal eviction of the now-disconnected guest
	if s.slots["cam-1"].occupant != "" {
		t.Fatalf("a terminal leave must vacate a grace-bound slot, got occupant=%q", s.slots["cam-1"].occupant)
	}
	if !hasFrameType(out, "slot-unbound") {
		t.Fatalf("a terminal leave of a grace-bound guest should emit slot-unbound, got %+v", out)
	}
}

// Moving an OFFLINE guest (one in its grace window) to a new cam slot must vacate the cam slot it
// still grace-holds — the one-cam-slot-per-occupant invariant (D-20) — so the old OBS source can't
// keep showing the moved guest while the DB/host UI say it moved. Both slots end as placeholders
// (the offline guest can't receive media yet; its /ws join replays the new slot).
func TestRebindOrVacateClearsGraceHeldSlotForOfflineGuest(t *testing.T) {
	s := newRoomState()
	s.join("src1", "obs", "")
	s.attachSource("cam-1", "src1")
	s.join("src2", "obs", "")
	s.attachSource("cam-2", "src2")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.leave("g1", false) // g1 drops → grace-held on cam-1
	if s.slots["cam-1"].occupant != "g1" {
		t.Fatal("precondition: g1 grace-held on cam-1")
	}

	// Host moves the OFFLINE guest to cam-2 (the live PUT path → RebindOrVacate; g1 not in s.peers).
	out := s.rebindOrVacate("cam-2", "g1")

	if s.slots["cam-1"].occupant != "" {
		t.Fatalf("moving the offline guest must vacate its grace-held cam-1, got occupant=%q", s.slots["cam-1"].occupant)
	}
	if s.slots["cam-2"].occupant != "" {
		t.Fatalf("cam-2 should be a placeholder for the offline guest, got occupant=%q", s.slots["cam-2"].occupant)
	}
	if !hasFrameType(out, "slot-unbound") {
		t.Fatalf("the freed sources should be sent slot-unbound, got %+v", out)
	}
}

// vacateOccupant (the greenroom unassign / the revoke + re-issue terminal paths) clears a cam slot
// the occupant grace-holds while disconnected, so a terminal action on an already-dropped guest
// doesn't leave the OBS source showing it until the grace timer expires (D-M5.5-3).
func TestVacateOccupantClearsGraceHeldSlot(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.leave("g1", false) // grace-held
	if s.slots["cam-1"].occupant != "g1" {
		t.Fatal("precondition: g1 grace-held on cam-1")
	}
	// The guest reconnects but its replay is SKIPPED (no valid binding to resume), so it sits in
	// s.peers with the slot still disconnected — the /ws join handler then vacates it explicitly.
	s.join("g1", "guest", "")

	out := s.vacateOccupant("g1")
	if s.slots["cam-1"].occupant != "" {
		t.Fatalf("vacateOccupant must clear a grace-held slot (even for a reconnected occupant), got occupant=%q", s.slots["cam-1"].occupant)
	}
	if s.slots["cam-1"].disconnected {
		t.Fatal("vacating must also clear the grace flag so no limbo binding remains")
	}
	if !hasFrameType(out, "slot-unbound") {
		t.Fatalf("the freed source should be sent slot-unbound, got %+v", out)
	}
}

// A terminal vacate during the grace window (host unbind, kick, displacement) clears the binding
// immediately, and the pending expiry no-ops (occupant changed/cleared).
func TestTerminalVacateDuringGraceDefusesExpiry(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")
	s.leave("g1", false)
	gen := s.slots["cam-1"].graceGen

	// Host explicitly unbinds the slot while the guest is in its grace window.
	if out := s.unbindSlot("cam-1"); !hasFrameType(out, "slot-unbound") {
		t.Fatalf("an explicit unbind during grace should vacate now, got %+v", out)
	}
	if s.slots["cam-1"].occupant != "" {
		t.Fatal("unbind should clear the occupant immediately")
	}
	if exp := s.expireGrace("cam-1", "g1", gen); exp != nil {
		t.Fatalf("expireGrace must no-op after a terminal vacate, got %+v", exp)
	}
}

// The screenshare slot is a live action, not a persistent identity (D-21): a sharer's drop vacates
// immediately (no grace) — only cam slots get the grace window.
func TestLeaveVacatesScreenSlotImmediately(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs_screen", "")
	s.attachSource("screen", "src")
	s.join("g1", "guest", "")
	s.setScreenEligible("g1", true)
	s.screenStart("g1")
	s.screenSelect("host", "g1") // host promotes g1's share live into the screen slot

	if s.slots["screen"].occupant != "g1" {
		t.Fatalf("precondition: g1 should hold the live screen slot, got %+v", s.slots["screen"])
	}
	out := s.leave("g1", false)
	st := s.slots["screen"]
	if st.occupant != "" || st.disconnected {
		t.Fatalf("a screen-slot occupant drop must vacate immediately (no grace), got %+v", st)
	}
	if !hasFrameType(out, "slot-unbound") {
		t.Fatalf("the screen source should get slot-unbound on the sharer's drop, got %+v", out)
	}
}
