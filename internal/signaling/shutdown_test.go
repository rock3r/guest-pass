package signaling

import (
	"testing"
	"time"
)

func TestHubShutdown_BroadcastsTerminateThenCloses(t *testing.T) {
	h := NewHub()
	room := h.Room("s1")
	out := make(chan Frame, 16)
	// Join is async (posted to the room's cmd channel); the FIFO channel guarantees it
	// runs before the Terminate command Shutdown posts, so the conn is registered first.
	room.Join(PeerID("p1"), "host", "", out)

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

	// The registry is cleared: a new Room for the same session is a fresh instance.
	if again := h.Room("s1"); again == room {
		t.Fatal("Shutdown should clear the room registry")
	}
}

// TestRoomTerminate_DoesNotDropTerminate uses an unbuffered out (no reader) so a send
// can only proceed once a reader appears. The terminate frame is terminal (RF-16) and
// must NOT be dropped: Terminate must block on the send rather than return immediately.
func TestRoomTerminate_DoesNotDropTerminate(t *testing.T) {
	r := newRoom("s")
	go r.run()
	defer r.Close()

	out := make(chan Frame) // unbuffered: Join's non-blocking roster deliver drops (no reader)
	r.Join(PeerID("p"), "guest", "", out)

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
