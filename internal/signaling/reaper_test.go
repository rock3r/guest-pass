package signaling

import (
	"errors"
	"testing"
)

// ParticipantCount counts greenroom participants (host/co-host/guest) and EXCLUDES OBS source
// pages, so the reaper reads a session held open only by a lingering source as idle (D-40).
func TestRoomParticipantCount_ExcludesSources(t *testing.T) {
	r := newRoom("s", nil, nil, 0)
	go r.run()
	defer r.Close()
	r.Join(PeerID("h"), "host", "", "", make(chan Frame, 8))
	r.Join(PeerID("g"), "guest", "", "", make(chan Frame, 8))
	r.Join(PeerID("src-cam-1"), "obs", "", "cam-1", make(chan Frame, 8))

	if got := r.ParticipantCount(); got != 2 {
		t.Fatalf("ParticipantCount = %d, want 2 (host+guest, OBS source excluded)", got)
	}
}

// TerminateIfIdle aborts (returns false, leaves the room intact) when a participant is connected —
// so a reconnect in the poll→reap race is never reaped.
func TestRoomTerminateIfIdle_AbortsWithParticipant(t *testing.T) {
	r := newRoom("s", nil, nil, 0)
	go r.run()
	defer r.Close()
	out := make(chan Frame, 8)
	r.Join(PeerID("g"), "guest", "", "", out)

	if r.TerminateIfIdle() {
		t.Fatal("TerminateIfIdle should return false while a participant is connected")
	}
	// The room is untouched: the participant is still counted and was not terminated.
	if got := r.ParticipantCount(); got != 1 {
		t.Fatalf("after aborted reap, ParticipantCount = %d, want 1", got)
	}
	// Drain whatever Join delivered (a roster frame) and assert NO terminate frame appears and
	// the channel was not closed (an aborted reap tears down nothing).
	for {
		select {
		case f, ok := <-out:
			if !ok {
				t.Fatal("aborted reap must not close the participant's connection")
			}
			if f.T == "terminate" {
				t.Fatalf("aborted reap must not send a terminate, got %+v", f)
			}
		default:
			return // drained; no terminate, channel still open
		}
	}
}

// TerminateIfIdle reaps when only OBS sources remain (no participants): it returns true and gives
// each source a RECOVERABLE reconnect (wire-once pages outlive the session).
func TestRoomTerminateIfIdle_ReapsSourceOnly(t *testing.T) {
	r := newRoom("s", nil, nil, 0)
	go r.run()
	defer r.Close()
	srcOut := make(chan Frame, 8)
	r.Join(PeerID("src-cam-1"), "obs", "", "cam-1", srcOut)

	if !r.TerminateIfIdle() {
		t.Fatal("TerminateIfIdle should reap when only OBS sources remain")
	}
	if got := lastTerminateReason(srcOut); got != TerminateReconnect {
		t.Fatalf("source terminate reason = %q, want recoverable %q", got, TerminateReconnect)
	}
}

// TerminateIfIdle reaps a fully empty room (no conns at all).
func TestRoomTerminateIfIdle_ReapsEmpty(t *testing.T) {
	r := newRoom("s", nil, nil, 0)
	go r.run()
	defer r.Close()
	if !r.TerminateIfIdle() {
		t.Fatal("TerminateIfIdle should reap an empty room")
	}
}

// Hub.ParticipantCount: 0 when no room is live; the room's participant count otherwise.
func TestHubParticipantCount(t *testing.T) {
	h := NewHub(nil, nil)
	if got := h.ParticipantCount("nobody"); got != 0 {
		t.Fatalf("ParticipantCount with no room = %d, want 0", got)
	}
	r := h.Room("s1")
	r.Join(PeerID("g"), "guest", "", "", make(chan Frame, 8))
	if got := h.ParticipantCount("s1"); got != 1 {
		t.Fatalf("ParticipantCount = %d, want 1", got)
	}
}

// TerminateHostRoom (account erasure, AC-5) sends EVERY peer — participants AND OBS sources — the
// TERMINAL reason, so a source STOPS rather than reconnect-looping against the about-to-be-deleted
// slot token (codex). Contrast EndSession, which gives sources a recoverable reconnect.
func TestHubTerminateHostRoom_SourcesGetTerminalReason(t *testing.T) {
	h := NewHub(nil, nil)
	r := h.Room("s1")
	guestOut := make(chan Frame, 8)
	srcOut := make(chan Frame, 8)
	r.Join(PeerID("g"), "guest", "", "", guestOut)
	r.Join(PeerID("src-cam-1"), "obs", "", "cam-1", srcOut)

	h.TerminateHostRoom("s1", TerminateSessionEnded)

	if got := lastTerminateReason(guestOut); got != TerminateSessionEnded {
		t.Fatalf("participant terminate = %q, want %q", got, TerminateSessionEnded)
	}
	if got := lastTerminateReason(srcOut); got != TerminateSessionEnded {
		t.Fatalf("OBS source terminate = %q, want the TERMINAL %q (not a recoverable reconnect)", got, TerminateSessionEnded)
	}
	if h.RoomIfLive("s1") != nil {
		t.Fatal("terminated room must be deregistered")
	}
}

// Hub.ReapIfIdle reaps and runs onReaped when no room is live — the post-restart orphan case (a
// session active since before a restart, with no rebuilt room). It spawns a transient gate room so
// the DB end is still serialized against reconnects.
func TestHubReapIfIdle_NoRoom(t *testing.T) {
	h := NewHub(nil, nil)
	ran := false
	reaped, err := h.ReapIfIdle("nobody", func() error { ran = true; return nil })
	if !reaped || err != nil {
		t.Fatalf("ReapIfIdle with no room = (%v, %v), want (true, nil)", reaped, err)
	}
	if !ran {
		t.Fatal("onReaped (the DB session-end) must run even when no room was live")
	}
	if h.RoomIfLive("nobody") != nil {
		t.Fatal("the transient gate room must be deregistered after the reap")
	}
}

// Hub.ReapIfIdle aborts (false) and keeps the room when a participant is connected; onReaped must
// NOT run (the session must not be ended).
func TestHubReapIfIdle_AbortsWithParticipant(t *testing.T) {
	h := NewHub(nil, nil)
	r := h.Room("s1")
	r.Join(PeerID("g"), "guest", "", "", make(chan Frame, 8))

	ran := false
	reaped, _ := h.ReapIfIdle("s1", func() error { ran = true; return nil })
	if reaped {
		t.Fatal("ReapIfIdle should return false while a participant is connected")
	}
	if ran {
		t.Fatal("onReaped must not run on an aborted reap")
	}
	if h.RoomIfLive("s1") != r {
		t.Fatal("an aborted reap must leave the room in the registry")
	}
}

// Hub.ReapIfIdle reaps an idle room (only an OBS source): returns true, runs onReaped, deregisters
// the room, and the source gets a recoverable reconnect so it re-attaches to the next session.
func TestHubReapIfIdle_ReapsIdleRoom(t *testing.T) {
	h := NewHub(nil, nil)
	r := h.Room("s1")
	srcOut := make(chan Frame, 8)
	r.Join(PeerID("src-cam-1"), "obs", "", "cam-1", srcOut)

	ran := false
	reaped, err := h.ReapIfIdle("s1", func() error { ran = true; return nil })
	if !reaped || err != nil || !ran {
		t.Fatalf("ReapIfIdle = (%v, %v) ran=%v, want reaped+onReaped", reaped, err, ran)
	}
	if h.RoomIfLive("s1") != nil {
		t.Fatal("reaped room must be removed from the registry")
	}
	if got := lastTerminateReason(srcOut); got != TerminateReconnect {
		t.Fatalf("source terminate reason = %q, want recoverable %q", got, TerminateReconnect)
	}
}

// Hub.ReapIfIdle surfaces the onReaped error (the DB session-end failure) while still tearing down
// the room — so the reaper logs it and retries next sweep.
func TestHubReapIfIdle_OnReapedErrorSurfaced(t *testing.T) {
	h := NewHub(nil, nil)
	r := h.Room("s1")
	r.Join(PeerID("src-cam-1"), "obs", "", "cam-1", make(chan Frame, 8))
	boom := errors.New("db down")
	reaped, err := h.ReapIfIdle("s1", func() error { return boom })
	if !reaped || !errors.Is(err, boom) {
		t.Fatalf("ReapIfIdle = (%v, %v), want (true, boom)", reaped, err)
	}
}

// The race fix (codex/bugbot): while onReaped (the DB session-end) runs, the room is STILL
// registered and terminating, so a reconnecting participant resolving it via hub.Room is refused —
// it can't spawn/join a fresh room for the session being ended.
func TestHubReapIfIdle_RefusesReconnectDuringDBEnd(t *testing.T) {
	h := NewHub(nil, nil)
	r := h.Room("s1")
	r.Join(PeerID("src-cam-1"), "obs", "", "cam-1", make(chan Frame, 8)) // source-only → idle

	var stillRegistered bool
	var joinedDuring bool
	reaped, _ := h.ReapIfIdle("s1", func() error {
		room := h.RoomIfLive("s1") // a reconnect resolves the room the same way
		stillRegistered = room == r
		if room != nil {
			joinedDuring = room.Join(PeerID("host"), "host", "", "", make(chan Frame, 8))
		}
		return nil
	})
	if !reaped {
		t.Fatal("should reap an idle source-only room")
	}
	if !stillRegistered {
		t.Fatal("the room must stay registered during the DB end (so reconnects resolve to it)")
	}
	if joinedDuring {
		t.Fatal("a participant Join during the DB end must be refused (room terminating)")
	}
}
