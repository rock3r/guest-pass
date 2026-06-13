package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/rock3r/guest-pass/internal/signaling"
)

func dial(t *testing.T, base, qs string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(base, "http") + "/ws?" + qs
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", qs, err)
	}
	return c
}

func readFrame(t *testing.T, c *websocket.Conn) signaling.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var f signaling.Frame
	if err := wsjson.Read(ctx, c, &f); err != nil {
		t.Fatalf("read: %v", err)
	}
	return f
}

func write(t *testing.T, c *websocket.Conn, f signaling.Frame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c, f); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// A joining peer's first frame is the {t:"ice"} join-ack carrying the configured ICE
// servers (AD-14), so the client can build its RTCPeerConnection before any signaling.
func TestWSJoinAckCarriesICEConfig(t *testing.T) {
	hub := signaling.NewHub()
	ice := []signaling.ICEServer{{URLs: []string{"stun:stun.example.org:3478"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ServeWS(hub, nil, ice))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := dial(t, srv.URL, "session=s1&peer=g1&role=guest")
	defer g.CloseNow()

	f := readFrame(t, g)
	if f.T != "ice" {
		t.Fatalf("first frame = %q, want ice", f.T)
	}
	if len(f.ICEServers) != 1 || len(f.ICEServers[0].URLs) != 1 || f.ICEServers[0].URLs[0] != "stun:stun.example.org:3478" {
		t.Fatalf("ice frame servers = %+v, want one STUN entry", f.ICEServers)
	}
}

// Full transport path: an OBS source page connects for a slot, a host rebinds it to
// a guest, and the source page receives the slot-rebind over the wire (EN-3).
func TestWSSlotRebindEndToEnd(t *testing.T) {
	hub := signaling.NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ServeWS(hub, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	src := dial(t, srv.URL, "session=s1&peer=src&role=obs&slot=cam-1")
	defer src.CloseNow()
	if f := readFrame(t, src); f.T != "slot-unbound" {
		t.Fatalf("source's first frame = %q, want slot-unbound", f.T)
	}

	g1 := dial(t, srv.URL, "session=s1&peer=g1&role=guest")
	defer g1.CloseNow()

	host := dial(t, srv.URL, "session=s1&peer=host&role=host")
	defer host.CloseNow()
	write(t, host, signaling.Frame{T: "rebind", Slot: "cam-1", OccupantPeerID: "g1"})

	f := readFrame(t, src)
	if f.T != "slot-rebind" || f.OccupantPeerID != "g1" || f.Epoch != 1 {
		t.Fatalf("source's rebind frame = %+v, want slot-rebind(g1, epoch 1)", f)
	}
}

// On a graceful drain (Hub.Shutdown), connected peers receive a terminate:reconnect
// frame over the wire before the socket closes (RF-21).
func TestWSDrainSendsTerminate(t *testing.T) {
	hub := signaling.NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ServeWS(hub, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// A source conn gets slot-unbound immediately on join, confirming the room is
	// registered before we trigger the drain (avoids a join/shutdown race).
	c := dial(t, srv.URL, "session=d&peer=src&role=obs&slot=cam-1")
	defer c.CloseNow()
	if f := readFrame(t, c); f.T != "slot-unbound" {
		t.Fatalf("first frame = %q, want slot-unbound", f.T)
	}

	hub.Shutdown("reconnect")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var f signaling.Frame
		if err := wsjson.Read(ctx, c, &f); err != nil {
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

// SDP/ICE is relayed verbatim between addressed peers, stamped with the sender.
func TestWSSignalRelay(t *testing.T) {
	hub := signaling.NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ServeWS(hub, nil, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := dial(t, srv.URL, "session=r&peer=a&role=guest")
	defer a.CloseNow()
	b := dial(t, srv.URL, "session=r&peer=b&role=guest")
	defer b.CloseNow()

	write(t, a, signaling.Frame{T: "signal", To: "b", SDP: []byte(`"offer"`)})
	f := readFrame(t, b)
	if f.T != "signal" || f.From != "a" {
		t.Fatalf("relayed = %+v, want signal stamped from=a", f)
	}
}
