package web

import (
	"testing"

	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

func webBoolPtr(b bool) *bool { return &b }

// lockOf returns a roster entry's lock of the given kind, or false.
func lockOf(e signaling.RosterEntry, kind string) (signaling.LockView, bool) {
	for _, l := range e.Locks {
		if l.Kind == kind {
			return l, true
		}
	}
	return signaling.LockView{}, false
}

// awaitRosterLock reads roster frames from c until the entry for `id` carries a lock of `kind`.
func awaitRosterLock(t *testing.T, c *websocket.Conn, id, kind string) signaling.RosterEntry {
	t.Helper()
	for i := 0; i < 12; i++ {
		r := wsReadFrameOfType(t, c, "roster")
		for _, e := range r.Peers {
			if e.ID == id {
				if _, ok := lockOf(e, kind); ok {
					return e
				}
			}
		}
	}
	t.Fatalf("no roster with a %s lock on %s arrived", kind, id)
	return signaling.RosterEntry{}
}

// The force/release dispatch maps each frame type to the right lock modality (D-13).
func TestForceModalityMapping(t *testing.T) {
	for in, want := range map[string]string{
		"force-mute": "mic", "force-no-cam": "cam", "force-no-share": "share", "other": "",
	} {
		if got := forceModality(in); got != want {
			t.Fatalf("forceModality(%q) = %q, want %q", in, got, want)
		}
	}
	if isLockModality("bogus") || !isLockModality("mic") || !isLockModality("cam") || !isLockModality("share") {
		t.Fatalf("isLockModality must accept mic/cam/share only")
	}
}

// A host force-mute over the WS locks the guest's mic in the roster (D-13), and the guest's
// attempt to self-unmute is rejected with an authoritative re-broadcast (EN-7). Exercises the
// force dispatch wiring (modality mapping + peerId target + reducer enforcement) end to end.
func TestWS_ForceMuteLocksAndRejectsSelfEnable(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	hc := h.dialOK(t, "", cookieHeader(cookie))
	defer hc.CloseNow()
	wsReadFrameOfType(t, hc, "roster")

	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	wsReadFrameOfType(t, gc, "roster")      // guest join roster
	wsReadFrameOfType(t, hc, "peer-joined") // sync: the host knows the guest is in the room

	// Host force-mutes the guest; the guest's own (self) entry shows a host-applied mic lock.
	wsWriteFrame(t, hc, signaling.Frame{T: "force-mute", PeerID: pass.ID})
	e := awaitRosterLock(t, gc, pass.ID, "mic")
	if l, _ := lockOf(e, "mic"); l.ApplierRank != "host" {
		t.Fatalf("guest should see its mic locked by the host, got locks=%+v", e.Locks)
	}
	if e.Mic {
		t.Fatalf("the force-muted guest's mic must be suppressed in the roster, got %+v", e)
	}

	// The guest tries to self-unmute → rejected; an authoritative roster still shows it suppressed.
	wsWriteFrame(t, gc, signaling.Frame{T: "state", Mic: webBoolPtr(true)})
	if e2 := awaitRosterLock(t, gc, pass.ID, "mic"); e2.Mic {
		t.Fatalf("a force-muted guest must not be able to self-unmute, got %+v", e2)
	}
}
