package signaling

import (
	"testing"
	"time"
)

func recvFrame(t *testing.T, ch chan Frame) Frame {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return Frame{}
	}
}

// recvFrameOfType drains roster/peer-joined/peer-left bookkeeping frames (EN-8) and
// returns the first frame of the wanted type, failing on close or timeout.
func recvFrameOfType(t *testing.T, ch chan Frame, want string) Frame {
	t.Helper()
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed before a %q frame arrived", want)
			}
			if f.T == want {
				return f
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for a %q frame", want)
		}
	}
}

// assertNoFrameWithin fails if any frame arrives on ch before d elapses (used to assert the source
// stays bound — no slot-unbound — during the grace window).
func assertNoFrameWithin(t *testing.T, ch chan Frame, d time.Duration) {
	t.Helper()
	select {
	case f := <-ch:
		t.Fatalf("expected no frame within %s, got %+v", d, f)
	case <-time.After(d):
	}
}

// drainFrames consumes any frames already queued on ch (e.g. the occupant-locks frame bindSlot
// sends to a source alongside slot-rebind), so a following no-frame assertion sees only new frames.
func drainFrames(ch chan Frame) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// D-40/AC-3: a guest's transient WS drop keeps its cam-slot binding for the grace window — the OBS
// source gets NO slot-unbound during it — and the slot vacates (slot-unbound) only after the window
// expires with no rejoin. Driven through the real Room actor + its grace timer with a short window.
func TestRoomGraceRetainsBindingThenVacatesOnExpiry(t *testing.T) {
	const grace = 80 * time.Millisecond
	r := newRoom("grace", nil, nil, grace)
	go r.run()
	defer r.Close()

	srcOut := make(chan Frame, 8)
	r.Join("src", "obs", "", "cam-1", srcOut)
	recvFrameOfType(t, srcOut, "slot-unbound") // initial attach: no occupant yet

	g1Out := make(chan Frame, 8)
	r.Join("g1", "guest", "", "", g1Out)
	r.Rebind("cam-1", "g1")
	recvFrameOfType(t, srcOut, "slot-rebind") // source now bound to g1
	drainFrames(srcOut)

	r.Leave("g1", g1Out) // transient drop (socket close)

	// During the grace window the binding is retained: the source must not be sent slot-unbound.
	assertNoFrameWithin(t, srcOut, grace/2)
	// After the window with no rejoin, the slot vacates and the source falls to placeholder.
	recvFrameOfType(t, srcOut, "slot-unbound")
}

// D-40/AC-3: a rejoin within the grace window resumes the slot (slot-rebind) and defuses the
// expiry, so the OBS source is NEVER sent slot-unbound — no placeholder flash, no host action.
func TestRoomGraceRejoinResumesWithoutVacate(t *testing.T) {
	const grace = 60 * time.Millisecond
	r := newRoom("grace2", nil, nil, grace)
	go r.run()
	defer r.Close()

	srcOut := make(chan Frame, 8)
	r.Join("src", "obs", "", "cam-1", srcOut)
	recvFrameOfType(t, srcOut, "slot-unbound")

	g1Out := make(chan Frame, 8)
	r.Join("g1", "guest", "", "", g1Out)
	r.Rebind("cam-1", "g1")
	recvFrameOfType(t, srcOut, "slot-rebind")
	drainFrames(srcOut)

	r.Leave("g1", g1Out) // transient drop

	// The guest reconnects within the window: re-register + replay its persisted binding.
	g1Out2 := make(chan Frame, 8)
	r.Join("g1", "guest", "", "", g1Out2)
	r.ResumeBind("cam-1", "g1")
	recvFrameOfType(t, srcOut, "slot-rebind") // the source re-links to the rejoined occupant
	drainFrames(srcOut)

	// Well past the original window, the defused expiry must never have sent slot-unbound.
	assertNoFrameWithin(t, srcOut, 3*grace)
}

// A terminal eviction of a guest that is CURRENTLY in its grace window vacates its grace-bound slot
// at once (the source gets slot-unbound) rather than leaving a zombie binding until the grace timer
// — even though the guest has no live connection to evict. Long grace so only the evict can vacate.
func TestRoomEvictPeersVacatesGraceBoundSlot(t *testing.T) {
	r := newRoom("evictgrace", nil, nil, 5*time.Second)
	go r.run()
	defer r.Close()

	srcOut := make(chan Frame, 8)
	r.Join("src", "obs", "", "cam-1", srcOut)
	recvFrameOfType(t, srcOut, "slot-unbound")
	g1Out := make(chan Frame, 8)
	r.Join("g1", "guest", "", "", g1Out)
	r.Rebind("cam-1", "g1")
	recvFrameOfType(t, srcOut, "slot-rebind")
	drainFrames(srcOut)

	r.Leave("g1", g1Out)                                 // transient drop → grace-bound (won't expire for 5s)
	assertNoFrameWithin(t, srcOut, 100*time.Millisecond) // grace retains: no slot-unbound yet
	r.EvictPeers("session-ended", []PeerID{"g1"})        // terminal eviction of the disconnected guest
	recvFrameOfType(t, srcOut, "slot-unbound")           // the grace-bound slot vacates NOW
}

// The actor delivers a slot-rebind to the source page's channel when the host
// rebinds the slot — end to end through the command channel and delivery.
func TestRoomDeliversSlotRebindToSource(t *testing.T) {
	r := newRoom("s1", nil, nil, 0)
	go r.run()
	defer r.Close()

	srcOut := make(chan Frame, 8)
	r.Join("src", "obs", "", "cam-1", srcOut)
	if f := recvFrame(t, srcOut); f.T != "slot-unbound" {
		t.Fatalf("source's initial frame = %q, want slot-unbound", f.T)
	}

	g1Out := make(chan Frame, 8)
	r.Join("g1", "guest", "", "", g1Out)
	r.Rebind("cam-1", "g1")

	f := recvFrame(t, srcOut)
	if f.T != "slot-rebind" || f.OccupantPeerID != "g1" || epochVal(f) != 1 {
		t.Fatalf("rebind frame = %+v, want slot-rebind(g1, epoch 1)", f)
	}
}

// One connection per identity (EN-16): a second Join with the same id evicts the
// first (closes its out), and the evicted connection's Leave must NOT tear down the
// connection that supplanted it.
func TestDuplicateIdEvictsPriorAndLeaveIsIdentityChecked(t *testing.T) {
	r := newRoom("dup", nil, nil, 0)
	go r.run()
	defer r.Close()

	out1 := make(chan Frame, 8)
	r.Join("g1", "guest", "", "", out1)

	out2 := make(chan Frame, 8)
	r.Join("g1", "guest", "", "", out2) // same id → evicts out1

	// out1 must be closed by the eviction.
	select {
	case _, ok := <-out1:
		for ok {
			_, ok = <-out1
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prior connection's out was not closed on eviction")
	}

	// The stale connection leaving must be a no-op for the live one.
	r.Leave("g1", out1)

	// out2 is still the live g1: a relayed signal reaches it (past the roster/peer-joined
	// bookkeeping frames that join now emits).
	r.Join("peer", "guest", "", "", make(chan Frame, 8))
	r.Signal("peer", Frame{T: "signal", To: "g1", SDP: []byte(`"x"`)})
	if f := recvFrameOfType(t, out2, "signal"); f.From != "peer" {
		t.Fatalf("live connection should still receive the relayed signal, got %+v", f)
	}
}

func TestRoomRelaysSignalBetweenPeers(t *testing.T) {
	r := newRoom("s2", nil, nil, 0)
	go r.run()
	defer r.Close()

	aOut := make(chan Frame, 8)
	bOut := make(chan Frame, 8)
	r.Join("a", "guest", "", "", aOut)
	r.Join("b", "guest", "", "", bOut)

	r.Signal("a", Frame{T: "signal", To: "b", SDP: []byte(`"offer"`)})
	f := recvFrameOfType(t, bOut, "signal") // past b's roster frame
	if f.From != "a" {
		t.Fatalf("relayed frame = %+v, want signal from=a", f)
	}
}

// AD-13 (end to end): a reported audio level is coalesced onto the room's batched {t:levels}
// tick — the ticker fires on the room goroutine and delivers the meter map, proving the wiring
// (applyState stores level → tick → buildLevels → deliver), not just the pure reducer.
func TestRoomLevelsTickDelivers(t *testing.T) {
	r := newRoom("lvl", nil, nil, 0)
	go r.run()
	defer r.Close()

	out := make(chan Frame, 16)
	r.Join("g1", "guest", "", "", out)
	r.ApplyState("g1", nil, nil, nil, fptr(0.7)) // a meter-only {t:state}

	f := recvFrameOfType(t, out, "levels") // past the join roster frame
	if f.Levels["g1"] != 0.7 {
		t.Fatalf("levels tick should carry g1=0.7, got %+v", f.Levels)
	}
}
