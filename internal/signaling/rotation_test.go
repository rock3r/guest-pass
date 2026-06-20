package signaling

import (
	"testing"
	"time"
)

// AC-5: rotating a slot token tears down its live OBS source — the source receives a
// TERMINAL {t:terminate,token-rotated} and is evicted; a participant is never terminated by
// this path, and rotating an offline/absent source is a no-op.
func TestRoomRotateSourceTerminatesAndEvicts(t *testing.T) {
	r := newRoom("rot", nil, nil, 0)
	go r.run()
	defer r.Close()

	srcOut := make(chan Frame, 8)
	r.Join("src-cam-1", "obs", "", "cam-1", srcOut)
	if f := recvFrame(t, srcOut); f.T != "slot-unbound" {
		t.Fatalf("source's initial frame = %q, want slot-unbound", f.T)
	}

	r.RotateSource("src-cam-1")
	if f := recvFrameOfType(t, srcOut, "terminate"); f.Reason != TerminateTokenRotated {
		t.Fatalf("terminate reason = %q, want token-rotated", f.Reason)
	}
	// The eviction closes the source's out channel.
	select {
	case _, ok := <-srcOut:
		for ok {
			_, ok = <-srcOut
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source out was not closed after rotation")
	}

	// Rotating an absent source is a no-op; a participant is never terminated here.
	gOut := make(chan Frame, 8)
	r.Join("g1", "guest", "", "", gOut)
	r.RotateSource("src-cam-1") // already gone
	r.RotateSource("g1")        // a participant — must be ignored
	r.Join("p2", "guest", "", "", make(chan Frame, 8))
	r.Signal("p2", Frame{T: "signal", To: "g1", SDP: []byte(`"x"`)})
	if f := recvFrameOfType(t, gOut, "signal"); f.From != "p2" {
		t.Fatalf("participant must be untouched by RotateSource, got %+v", f)
	}
}

// The terminal token-rotated frame must survive a FULL out-queue (RF-16): a non-blocking send
// would drop it, the OBS page would see a bare close, treat it as transient, and reconnect-loop
// the dead token. With the budgeted send it is delivered once the writer drains a slot.
func TestRoomRotateSourceTerminalSurvivesFullQueue(t *testing.T) {
	r := newRoom("rotfull", nil, nil, 0)
	go r.run()
	defer r.Close()

	srcOut := make(chan Frame, 1) // tiny buffer
	r.Join("src-cam-1", "obs", "", "cam-1", srcOut)
	// Join enqueued the source's initial slot-unbound frame, so the buffer is now FULL. Rotate
	// WITHOUT draining first: the old non-blocking send would drop the terminal here.
	r.RotateSource("src-cam-1")

	// Drain: the buffered slot-unbound, then the terminal (delivered once a slot frees up).
	if f := recvFrame(t, srcOut); f.T != "slot-unbound" {
		t.Fatalf("first frame = %q, want the buffered slot-unbound", f.T)
	}
	if f := recvFrameOfType(t, srcOut, "terminate"); f.Reason != TerminateTokenRotated {
		t.Fatalf("terminate reason = %q, want token-rotated (not dropped on a full queue)", f.Reason)
	}
}

// TerminateSourceIfLive must not spawn a room when none is live (rotation while offline is a
// DB-only update).
func TestHubTerminateSourceIfLiveDoesNotSpawn(t *testing.T) {
	h := NewHub(nil, nil)
	h.TerminateSourceIfLive("ghost-session", "src-cam-1")
	h.mu.Lock()
	_, exists := h.rooms["ghost-session"]
	h.mu.Unlock()
	if exists {
		t.Fatal("TerminateSourceIfLive must not create a room when none is live")
	}
}

// When a room IS live, TerminateSourceIfLive routes the rotation to it and the source is torn
// down with token-rotated.
func TestHubTerminateSourceIfLiveTearsDownLiveSource(t *testing.T) {
	h := NewHub(nil, nil)
	room := h.Room("live-session")
	defer h.Shutdown("reconnect")

	srcOut := make(chan Frame, 8)
	room.Join("src-cam-2", "obs", "", "cam-2", srcOut)
	if f := recvFrame(t, srcOut); f.T != "slot-unbound" {
		t.Fatalf("initial frame = %q, want slot-unbound", f.T)
	}

	h.TerminateSourceIfLive("live-session", "src-cam-2")
	if f := recvFrameOfType(t, srcOut, "terminate"); f.Reason != TerminateTokenRotated {
		t.Fatalf("terminate reason = %q, want token-rotated", f.Reason)
	}
}
