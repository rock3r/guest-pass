package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// --- shared WS test helpers ---

func cookieHeader(c *http.Cookie) http.Header {
	return http.Header{"Cookie": {c.String()}}
}

func wsjsonRead(ctx context.Context, c *websocket.Conn, v *signaling.Frame) error {
	return wsjson.Read(ctx, c, v)
}

func wsWriteFrame(t *testing.T, c *websocket.Conn, f signaling.Frame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c, f); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// --- transport-level behaviors (migrated to credential auth) ---

// On a graceful drain (Hub.Shutdown), connected peers receive a terminate:reconnect
// frame over the wire before the socket closes (RF-21).
func TestWSDrainSendsTerminate(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	srcRaw, _ := h.seedCamSlot(t, host.ID, 1)

	// A source conn gets slot-unbound immediately on join, confirming the room is
	// registered before we trigger the drain (avoids a join/shutdown race).
	c := h.dialOK(t, "src="+srcRaw, nil)
	defer c.CloseNow()
	if f := wsReadFrame(t, c); f.T != "slot-unbound" {
		t.Fatalf("first frame = %q, want slot-unbound", f.T)
	}

	h.hub.Shutdown("reconnect")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var f signaling.Frame
		if err := wsjsonRead(ctx, c, &f); err != nil {
			t.Fatalf("socket closed before a terminate frame arrived: %v", err)
		}
		if f.T == "terminate" {
			if f.Reason != "reconnect" {
				t.Fatalf("terminate reason = %q, want reconnect", f.Reason)
			}
			return
		}
	}
}

// SDP/ICE is relayed verbatim between addressed peers, stamped with the sender. Both
// peers are guests in the same host's room (routed by session).
func TestWSSignalRelay(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	aRaw, aPass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	bRaw, bPass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	a := h.dialOK(t, "pass="+aRaw, nil)
	defer a.CloseNow()
	b := h.dialOK(t, "pass="+bRaw, nil)
	defer b.CloseNow()

	wsWriteFrame(t, a, signaling.Frame{T: "signal", To: bPass.ID, SDP: []byte(`"offer"`)})
	f := wsReadFrame(t, b)
	if f.T != "signal" || f.From != aPass.ID {
		t.Fatalf("relayed = %+v, want signal stamped from=%s", f, aPass.ID)
	}
}
