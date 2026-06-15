package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

// The host declares which stream is live via go-live / end-session (EN-2/D-20): start opens the
// active session (one per host), the detail page reflects it, a second start elsewhere is
// rejected, and end clears it. This is the runtime gate the /ws join-replay consults.
func TestSession_GoLiveAndEnd(t *testing.T) {
	ctx := context.Background()
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	x := a.createStream(t, alice, "Show X")
	y := a.createStream(t, alice, "Show Y")

	// Go live for X.
	if rec := a.req(t, http.MethodPost, "/app/streams/"+x+"/session/start", "", alice); rec.Code != http.StatusSeeOther {
		t.Fatalf("go live: status = %d, want 303", rec.Code)
	}
	if sess, err := a.store.ActiveSession(ctx, host.ID); err != nil || sess.StreamID != x {
		t.Fatalf("active session = %+v / %v, want stream X", sess, err)
	}
	// X's detail page shows the live indicator + an end control.
	if body := a.req(t, http.MethodGet, "/app/streams/"+x, "", alice).Body.String(); !strings.Contains(body, "End session") || !strings.Contains(body, "Live") {
		t.Fatalf("X detail page missing the live indicator/end control:\n%s", body)
	}

	// One live session per host: going live for Y while X is live is rejected, and Y's page
	// explains it rather than offering a second "Go live".
	if rec := a.req(t, http.MethodPost, "/app/streams/"+y+"/session/start", "", alice); rec.Code != http.StatusConflict {
		t.Fatalf("second go-live: status = %d, want 409", rec.Code)
	}
	if body := a.req(t, http.MethodGet, "/app/streams/"+y, "", alice).Body.String(); !strings.Contains(body, "live on another stream") {
		t.Fatalf("Y detail page should explain the one-live-at-a-time rule:\n%s", body)
	}

	// Ending from Y's page is a no-op (Y isn't the live one) — it must not kill X's session.
	if rec := a.req(t, http.MethodPost, "/app/streams/"+y+"/session/end", "", alice); rec.Code != http.StatusSeeOther {
		t.Fatalf("end from non-live page: status = %d, want 303", rec.Code)
	}
	if sess, err := a.store.ActiveSession(ctx, host.ID); err != nil || sess.StreamID != x {
		t.Fatalf("ending from Y wrongly disturbed the live session: %+v / %v", sess, err)
	}

	// End from X's page actually stops the session.
	if rec := a.req(t, http.MethodPost, "/app/streams/"+x+"/session/end", "", alice); rec.Code != http.StatusSeeOther {
		t.Fatalf("end: status = %d, want 303", rec.Code)
	}
	if _, err := a.store.ActiveSession(ctx, host.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after end: ActiveSession err = %v, want ErrNotFound", err)
	}
}

// Go-live is host-scoped (RF-2): a host can't start a session for someone else's stream.
func TestSession_GoLiveForeignStream404(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	x := a.createStream(t, alice, "Alice's Show")

	if rec := a.req(t, http.MethodPost, "/app/streams/"+x+"/session/start", "", bob); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign go-live: status = %d, want 404", rec.Code)
	}
}
