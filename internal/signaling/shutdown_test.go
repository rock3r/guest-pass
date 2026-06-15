package signaling

import (
	"testing"
	"time"
)

func TestHubShutdown_BroadcastsTerminateThenCloses(t *testing.T) {
	h := NewHub(nil, nil)
	room := h.Room("s1")
	out := make(chan Frame, 16)
	// Join is async (posted to the room's cmd channel); the FIFO channel guarantees it
	// runs before the Terminate command Shutdown posts, so the conn is registered first.
	room.Join(PeerID("p1"), "host", "", "", out)

	h.Shutdown("reconnect")

	var sawTerminate bool
	for f := range out { // ranges until Terminate closes the channel
		if f.T == "terminate" && f.Reason == "reconnect" {
			sawTerminate = true
		}
	}
	if !sawTerminate {
		t.Fatal("expected a terminate:reconnect frame before the peer's channel closed")
	}

	// After Shutdown the hub is closed: Room returns nil rather than spawning a new,
	// un-drained room.
	if again := h.Room("s1"); again != nil {
		t.Fatal("Room after Shutdown should return nil (hub closed)")
	}
	_ = room
}

func TestHubShutdown_NoNewRoomsAfterClose(t *testing.T) {
	h := NewHub(nil, nil)
	h.Shutdown("reconnect")
	if r := h.Room("late-arrival"); r != nil {
		t.Fatal("Room for a new session after Shutdown should be nil")
	}
}

// TestRoomEvictPeers_DoesNotDropTerminate mirrors the Terminate guarantee for the SYSTEM eviction
// path (codex): the terminal `revoked` frame must reach a slow peer (backed-up/unbuffered queue)
// before its socket closes, not be dropped like a routine frame — else the browser sees a bare
// close and follows the transient reconnect flow instead of the revoked teardown. An unbuffered
// out with no reader makes a non-blocking deliver drop; the budgeted blocking send must still land
// once a reader appears.
func TestRoomEvictPeers_DoesNotDropTerminate(t *testing.T) {
	r := newRoom("s", nil, nil)
	go r.run()
	defer r.Close()

	out := make(chan Frame) // unbuffered, no reader: a non-blocking send would drop the terminate
	r.Join(PeerID("g"), "guest", "", "", out)

	returned := make(chan struct{})
	go func() { r.EvictPeers(TerminateRevoked, []PeerID{"g"}); close(returned) }()

	// With no reader, the OLD non-blocking deliver would drop the terminate and EvictPeers would
	// return at once. The budgeted blocking send must still be waiting after a grace period.
	select {
	case <-returned:
		t.Fatal("EvictPeers returned without delivering the terminate (it was dropped)")
	case <-time.After(50 * time.Millisecond):
	}

	// A reader appears; the blocked terminal send proceeds with the revoked reason.
	f := <-out
	if f.T != "terminate" || f.Reason != TerminateRevoked {
		t.Fatalf("delivered frame = %+v, want terminate:revoked", f)
	}
	<-returned
}

// A connection that resolved a room just before it started draining must not be admitted
// after the terminate broadcast — Join refuses it so it can't strand itself.
func TestRoomJoin_RefusedAfterTerminate(t *testing.T) {
	r := newRoom("s", nil, nil)
	go r.run()
	defer r.Close()

	r.Terminate("reconnect") // marks the room terminating (does not stop the goroutine)

	out := make(chan Frame, 4)
	if r.Join(PeerID("late"), "guest", "", "", out) {
		t.Fatal("Join after Terminate should be refused")
	}
}

// TestRoomTerminate_DoesNotDropTerminate uses an unbuffered out (no reader) so a send
// can only proceed once a reader appears. The terminate frame is terminal (RF-16) and
// must NOT be dropped: Terminate must block on the send rather than return immediately.
func TestRoomTerminate_DoesNotDropTerminate(t *testing.T) {
	r := newRoom("s", nil, nil)
	go r.run()
	defer r.Close()

	out := make(chan Frame) // unbuffered: Join's non-blocking roster deliver drops (no reader)
	r.Join(PeerID("p"), "guest", "", "", out)

	returned := make(chan struct{})
	go func() { r.Terminate("reconnect"); close(returned) }()

	// With no reader, the OLD non-blocking send would drop and Terminate would return
	// immediately. The blocking send must still be waiting after a grace period.
	select {
	case <-returned:
		t.Fatal("Terminate returned without delivering the terminate frame (dropped)")
	case <-time.After(50 * time.Millisecond):
	}

	// A reader appears; the blocked terminate send proceeds.
	f := <-out
	if f.T != "terminate" || f.Reason != "reconnect" {
		t.Fatalf("delivered frame = %+v, want terminate:reconnect", f)
	}
	<-returned
}
