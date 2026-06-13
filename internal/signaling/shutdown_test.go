package signaling

import "testing"

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
