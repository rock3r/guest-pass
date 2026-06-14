package signaling

import (
	"testing"
	"time"
)

// AC-9/T-8: kick authority — the actor must be a participant STRICTLY above the target.
func TestCanKick(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("co2", "cohost", "")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")
	s.join("src", "obs", "")

	cases := []struct {
		actor, target PeerID
		want          bool
	}{
		{"host", "co", true}, {"host", "g1", true}, // host kicks below
		{"co", "g1", true},       // cohost kicks a guest
		{"g1", "g2", false},      // a guest kicks no one
		{"co", "co2", false},     // a peer (equal rank) can't
		{"co", "host", false},    // can't kick up
		{"g1", "host", false},    // the host is immune
		{"host", "src", false},   // an OBS source is not a kickable participant
		{"src", "g1", false},     // an OBS source can't kick
		{"host", "ghost", false}, // unknown target
		{"ghost", "g1", false},   // unknown actor
	}
	for _, c := range cases {
		if got := s.canKick(c.actor, c.target); got != c.want {
			t.Fatalf("canKick(%s,%s) = %v, want %v", c.actor, c.target, got, c.want)
		}
	}
}

// AC-9/T-8: kickPeer clears the target's occupied slot (epoch bump BEFORE the teardown, EN-3),
// broadcasts peer-left, sends the target a terminal {t:terminate,kicked} (EN-9), and removes it.
func TestKickPeerTearsDownAtomically(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1") // g1 occupies cam-1 (epoch 1)
	epochBefore := s.slots["cam-1"].epoch

	out := s.kickPeer("g1")

	st := s.slots["cam-1"]
	if st.occupant != "" || st.epoch != epochBefore+1 {
		t.Fatalf("kick must clear the slot + bump its epoch, got %+v", st)
	}
	if _, ok := firstFrameOfType(out, "src", "slot-unbound"); !ok {
		t.Fatalf("kick must tell the source the slot is unbound (placeholder, not the kicked guest)")
	}
	if pl, ok := firstFrameOfType(out, "host", "peer-left"); !ok || pl.PeerID != "g1" {
		t.Fatalf("kick must broadcast peer-left(g1), got %+v", out)
	}
	if term, ok := firstFrameOfType(out, "g1", "terminate"); !ok || term.Reason != TerminateKicked {
		t.Fatalf("kick must send the target {t:terminate,kicked}, got %+v", out)
	}
	if _, ok := s.peers["g1"]; ok {
		t.Fatalf("kick must remove the target from room state")
	}

	// EN-3 ordering: the slot-clear (epoch bump) precedes the terminate teardown.
	unbindIdx, termIdx := -1, -1
	for i, o := range out {
		if o.frame.T == "slot-unbound" && unbindIdx < 0 {
			unbindIdx = i
		}
		if o.frame.T == "terminate" {
			termIdx = i
		}
	}
	if unbindIdx < 0 || termIdx < 0 || unbindIdx > termIdx {
		t.Fatalf("the slot-clear (epoch bump) must precede the terminate (EN-3); unbind@%d term@%d", unbindIdx, termIdx)
	}
}

// AC-9/T-8: Room.Kick (authorized) runs the invalidate closure (token revocation) and then
// evicts the target — it gets terminate:kicked and its out channel closes; the host sees it left.
func TestRoomKickInvalidatesAndEvicts(t *testing.T) {
	r := newRoom("h", nil, discardLogger())
	go r.run()
	defer r.Close()

	hostOut := make(chan Frame, 32)
	r.Join("host", "host", "", "", hostOut)
	g1Out := make(chan Frame, 32)
	r.Join("g1", "guest", "", "", g1Out)
	recvFrameOfType(t, hostOut, "peer-joined") // sync: g1 is in

	invalidated := make(chan struct{}, 1)
	r.Kick("host", "g1", func() { invalidated <- struct{}{} })

	if f := recvFrameOfType(t, g1Out, "terminate"); f.Reason != TerminateKicked {
		t.Fatalf("the kicked target should get terminate:kicked, got %+v", f)
	}
	// The eviction closes the target's out channel (drain until closed).
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-g1Out:
			if !ok {
				goto evicted
			}
		case <-deadline:
			t.Fatal("the kicked target's connection was not evicted (out not closed)")
		}
	}
evicted:
	recvFrameOfType(t, hostOut, "peer-left")
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("an authorized kick must invalidate the target's token")
	}
}

// AC-9/T-8: an unauthorized kick (a guest targeting the host) is a no-op — no token invalidation,
// no teardown. A subsequent relayed signal (FIFO after the kick cmd) proves the kick already ran.
func TestRoomKickUnauthorizedIsNoOp(t *testing.T) {
	r := newRoom("h", nil, discardLogger())
	go r.run()
	defer r.Close()

	hostOut := make(chan Frame, 32)
	r.Join("host", "host", "", "", hostOut)
	g1Out := make(chan Frame, 32)
	r.Join("g1", "guest", "", "", g1Out)
	recvFrameOfType(t, hostOut, "peer-joined")

	var invalidated bool
	r.Kick("g1", "host", func() { invalidated = true })
	r.Signal("g1", Frame{T: "signal", To: "host", SDP: []byte(`"x"`)})
	recvFrameOfType(t, hostOut, "signal") // FIFO: the kick cmd already ran before this
	if invalidated {
		t.Fatalf("an unauthorized kick must NOT invalidate the token")
	}
}
