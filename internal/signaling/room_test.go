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

// The actor delivers a slot-rebind to the source page's channel when the host
// rebinds the slot — end to end through the command channel and delivery.
func TestRoomDeliversSlotRebindToSource(t *testing.T) {
	r := newRoom("s1")
	go r.run()
	defer r.Close()

	srcOut := make(chan Frame, 8)
	r.Join("src", "obs", "cam-1", srcOut)
	if f := recvFrame(t, srcOut); f.T != "slot-unbound" {
		t.Fatalf("source's initial frame = %q, want slot-unbound", f.T)
	}

	g1Out := make(chan Frame, 8)
	r.Join("g1", "guest", "", g1Out)
	r.Rebind("cam-1", "g1")

	f := recvFrame(t, srcOut)
	if f.T != "slot-rebind" || f.OccupantPeerID != "g1" || f.Epoch != 1 {
		t.Fatalf("rebind frame = %+v, want slot-rebind(g1, epoch 1)", f)
	}
}

// One connection per identity (EN-16): a second Join with the same id evicts the
// first (closes its out), and the evicted connection's Leave must NOT tear down the
// connection that supplanted it.
func TestDuplicateIdEvictsPriorAndLeaveIsIdentityChecked(t *testing.T) {
	r := newRoom("dup")
	go r.run()
	defer r.Close()

	out1 := make(chan Frame, 8)
	r.Join("g1", "guest", "", out1)

	out2 := make(chan Frame, 8)
	r.Join("g1", "guest", "", out2) // same id → evicts out1

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

	// out2 is still the live g1: a relayed signal reaches it.
	r.Join("peer", "guest", "", make(chan Frame, 8))
	r.Signal("peer", Frame{T: "signal", To: "g1", SDP: []byte(`"x"`)})
	select {
	case f, ok := <-out2:
		if !ok || f.T != "signal" {
			t.Fatalf("live connection should still receive; got ok=%v f=%+v", ok, f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live connection was wrongly torn down by the stale Leave")
	}
}

func TestRoomRelaysSignalBetweenPeers(t *testing.T) {
	r := newRoom("s2")
	go r.run()
	defer r.Close()

	aOut := make(chan Frame, 8)
	bOut := make(chan Frame, 8)
	r.Join("a", "guest", "", aOut)
	r.Join("b", "guest", "", bOut)

	r.Signal("a", Frame{T: "signal", To: "b", SDP: []byte(`"offer"`)})
	f := recvFrame(t, bOut)
	if f.T != "signal" || f.From != "a" {
		t.Fatalf("relayed frame = %+v, want signal from=a", f)
	}
}
