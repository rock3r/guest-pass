package web

import (
	"testing"

	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// awaitRosterRole reads roster frames from c until the entry for `id` carries the wanted role.
func awaitRosterRole(t *testing.T, c *websocket.Conn, id, role string) {
	t.Helper()
	for i := 0; i < 12; i++ {
		r := wsReadFrameOfType(t, c, "roster")
		for _, e := range r.Peers {
			if e.ID == id && e.Role == role {
				return
			}
		}
	}
	t.Fatalf("no roster showing %s as %q arrived", id, role)
}

// A host promote over the WS flips a guest to co-host in the roster (D-15). Exercises the
// {t:role} dispatch wiring (host-only gate + Role field + reducer authority) end to end.
func TestWS_HostPromotesGuest(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	hc := h.dialOK(t, "", cookieHeader(cookie))
	defer hc.CloseNow()
	wsReadFrameOfType(t, hc, "roster")

	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	wsReadFrameOfType(t, gc, "roster")
	wsReadFrameOfType(t, hc, "peer-joined") // sync: the host knows the guest is in the room

	wsWriteFrame(t, hc, signaling.Frame{T: "role", PeerID: pass.ID, Role: "cohost"})
	awaitRosterRole(t, gc, pass.ID, "cohost")
}

// A co-host attempting a promote over the WS is rejected server-side (host-only, D-15): the
// dispatch gate plus the reducer authority both deny it, so the target's role never changes.
func TestWS_CohostCannotPromote(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	coRaw, _ := h.seedPass(t, stream.ID, store.RoleCohost, store.PassSent, nil)
	gRaw, gPass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	cc := h.dialOK(t, "pass="+coRaw, nil)
	defer cc.CloseNow()
	wsReadFrameOfType(t, cc, "roster")

	gc := h.dialOK(t, "pass="+gRaw, nil)
	defer gc.CloseNow()
	wsReadFrameOfType(t, gc, "roster")
	// The co-host learns the guest joined, then tries to promote it.
	pj := wsReadFrameOfType(t, cc, "peer-joined")
	if pj.Peer == nil || pj.Peer.ID != gPass.ID {
		t.Fatalf("expected the guest's peer-joined, got %+v", pj)
	}
	wsWriteFrame(t, cc, signaling.Frame{T: "role", PeerID: gPass.ID, Role: "cohost"})

	// The guest sends a {t:state}; the resulting roster must still show it as a guest (the promote
	// was a no-op). Using a real subsequent frame avoids racing on "nothing happened".
	wsWriteFrame(t, gc, signaling.Frame{T: "state", Cam: webBoolPtr(true)})
	r := wsReadFrameOfType(t, gc, "roster")
	for _, e := range r.Peers {
		if e.ID == gPass.ID && e.Role != "guest" {
			t.Fatalf("a co-host must not be able to promote a guest, got role=%q", e.Role)
		}
	}
}
