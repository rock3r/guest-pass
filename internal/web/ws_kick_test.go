package web

import (
	"context"
	"net/http"
	"testing"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// AC-8/AC-9/T-8 (INT): a kick sends the target a terminal terminate:kicked, REVOKES its pass, and
// REFUSES its re-join at the handshake (D-25 cooperative teardown + reconnection block, RF-22).
func TestWS_KickRevokesAndRefusesRejoin(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	gRaw, gPass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	hc := h.dialOK(t, "", cookieHeader(cookie))
	defer hc.CloseNow()
	wsReadFrameOfType(t, hc, "roster")
	gc := h.dialOK(t, "pass="+gRaw, nil)
	wsReadFrameOfType(t, gc, "roster")
	wsReadFrameOfType(t, hc, "peer-joined") // sync: the host knows the guest is in

	// Host kicks the guest; the guest receives a terminal terminate:kicked (EN-9).
	wsWriteFrame(t, hc, signaling.Frame{T: "kick", PeerID: gPass.ID})
	if f := wsReadFrameOfType(t, gc, "terminate"); f.Reason != signaling.TerminateKicked {
		t.Fatalf("the kicked guest should get terminate:kicked, got %+v", f)
	}
	gc.CloseNow()

	// The revoke runs BEFORE the terminate is delivered, so the pass is already revoked.
	p, err := h.store.GetPassByTokenHash(context.Background(), h.hasher.Hash(gRaw))
	if err != nil {
		t.Fatalf("get kicked pass: %v", err)
	}
	if p.Status != store.PassRevoked {
		t.Fatalf("a kick must revoke the target's pass, got status=%q", p.Status)
	}

	// Refuse re-join: a reconnect with the kicked pass is rejected at the handshake (403).
	_, resp, err := h.dial(t, "pass="+gRaw, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a kicked pass must be refused on re-join (403); got resp=%v err=%v", resp, err)
	}
}

// T-8 (the transient half): a guest that simply disconnects (no kick, no terminate frame) keeps a
// joinable pass, so a reconnect is admitted — the absent-frame fallback is transient (RF-22).
func TestWS_NormalDisconnectAllowsRejoin(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	gRaw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	gc := h.dialOK(t, "pass="+gRaw, nil)
	wsReadFrameOfType(t, gc, "roster")
	gc.CloseNow() // a normal disconnect — no kick, no terminate frame

	// The pass is unchanged → a reconnect is admitted (transient, not refused).
	gc2 := h.dialOK(t, "pass="+gRaw, nil)
	defer gc2.CloseNow()
	wsReadFrameOfType(t, gc2, "roster")
}
