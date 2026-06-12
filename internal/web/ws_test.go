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

// Full transport path: an OBS source page connects for a slot, a host rebinds it to
// a guest, and the source page receives the slot-rebind over the wire (EN-3).
func TestWSSlotRebindEndToEnd(t *testing.T) {
	hub := signaling.NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ServeWS(hub))
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

// SDP/ICE is relayed verbatim between addressed peers, stamped with the sender.
func TestWSSignalRelay(t *testing.T) {
	hub := signaling.NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", ServeWS(hub))
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
