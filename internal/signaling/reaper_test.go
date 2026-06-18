package signaling

import "testing"

// ParticipantCount counts greenroom participants (host/co-host/guest) and EXCLUDES OBS source
// pages, so the reaper reads a session held open only by a lingering source as idle (D-40).
func TestRoomParticipantCount_ExcludesSources(t *testing.T) {
	r := newRoom("s", nil, nil)
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
	r := newRoom("s", nil, nil)
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
	r := newRoom("s", nil, nil)
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
	r := newRoom("s", nil, nil)
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

// Hub.ReapIfIdle returns true (nothing to tear down) when no room is live — the caller still
// ends the DB session (e.g. a session active since before a restart, with no rebuilt room).
func TestHubReapIfIdle_NoRoom(t *testing.T) {
	h := NewHub(nil, nil)
	if !h.ReapIfIdle("nobody") {
		t.Fatal("ReapIfIdle with no room should return true (nothing to tear down)")
	}
}

// Hub.ReapIfIdle aborts (false) and keeps the room when a participant is connected.
func TestHubReapIfIdle_AbortsWithParticipant(t *testing.T) {
	h := NewHub(nil, nil)
	r := h.Room("s1")
	r.Join(PeerID("g"), "guest", "", "", make(chan Frame, 8))

	if h.ReapIfIdle("s1") {
		t.Fatal("ReapIfIdle should return false while a participant is connected")
	}
	if h.RoomIfLive("s1") != r {
		t.Fatal("an aborted reap must leave the room in the registry")
	}
}

// Hub.ReapIfIdle reaps an idle room (only an OBS source) — returns true, deregisters the room,
// and the source gets a recoverable reconnect so it re-attaches to the next session.
func TestHubReapIfIdle_ReapsIdleRoom(t *testing.T) {
	h := NewHub(nil, nil)
	r := h.Room("s1")
	srcOut := make(chan Frame, 8)
	r.Join(PeerID("src-cam-1"), "obs", "", "cam-1", srcOut)

	if !h.ReapIfIdle("s1") {
		t.Fatal("ReapIfIdle should reap a room with no participants")
	}
	if h.RoomIfLive("s1") != nil {
		t.Fatal("reaped room must be removed from the registry")
	}
	if got := lastTerminateReason(srcOut); got != TerminateReconnect {
		t.Fatalf("source terminate reason = %q, want recoverable %q", got, TerminateReconnect)
	}
}
