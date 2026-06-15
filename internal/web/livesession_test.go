package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rock3r/guest-pass/internal/signaling"
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

// Ending the session tears down the live room (codex P1/D-40): connected guests/OBS receive the
// terminal session-ended teardown and the room is removed, so no connection carries into the next
// stream (rooms are keyed by host id).
func TestSession_EndTerminatesRoom(t *testing.T) {
	ctx := context.Background()
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "ender", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	if _, err := h.store.StartSession(ctx, stream.ID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// A guest connects → the room spawns.
	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	_ = wsReadFrame(t, gc) // initial roster: the join completed, the room is live
	if h.hub.RoomIfLive(host.ID) == nil {
		t.Fatal("room should be live after the guest joined")
	}

	// Host ends the session (POST-redirect-GET; don't chase the redirect).
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/app/streams/"+stream.ID+"/session/end", nil)
	req.AddCookie(cookie)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("end POST: %v", err)
	}
	_ = resp.Body.Close()

	// The guest receives the terminal session-ended teardown...
	if f := readFrameOfType(t, gc, "terminate"); f.Reason != signaling.TerminateSessionEnded {
		t.Fatalf("terminate reason = %q, want %q", f.Reason, signaling.TerminateSessionEnded)
	}
	// ...the DB session is ended, and the room is gone (a fresh one would spawn for the next stream).
	if _, err := h.store.ActiveSession(ctx, host.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session not ended in DB: %v", err)
	}
	if h.hub.RoomIfLive(host.ID) != nil {
		t.Fatal("room must be removed on end-session, else connections carry into the next stream")
	}
}

// Going live reconciles an already-spawned room with persisted bindings (codex P2/D-40): a guest
// that connected and was bound BEFORE Go live (replay gated off, picker DB-only) gets routed onto
// its slot the moment the host goes live, without re-picking.
func TestSession_GoLiveReplaysPreLiveBindings(t *testing.T) {
	ctx := context.Background()
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "prelive", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	srcRaw, _ := h.seedCamSlot(t, host.ID, 1)

	// Guest + OBS source connect BEFORE Go live: no active session, so no replay fires.
	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	_ = wsReadFrame(t, gc)
	sc := h.dialOK(t, "src="+srcRaw, http.Header{"Origin": {"null"}})
	defer sc.CloseNow()
	if f := wsReadFrame(t, sc); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}

	// Host binds the guest to cam-1 via the picker — DB-only, since the stream isn't live yet.
	req, _ := http.NewRequest(http.MethodPut, h.srv.URL+"/api/passes/"+pass.ID+"/slot", strings.NewReader(`{"slot":"cam-1"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = resp.Body.Close()
	if got, _ := h.store.GetPass(ctx, pass.ID); got.SlotID == nil {
		t.Fatal("pre-live bind should persist in the DB")
	}

	// Host goes live → the pre-live binding is replayed → the source re-routes to the guest.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	greq, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/app/streams/"+stream.ID+"/session/start", nil)
	greq.AddCookie(cookie)
	gresp, err := noRedirect.Do(greq)
	if err != nil {
		t.Fatalf("go-live POST: %v", err)
	}
	_ = gresp.Body.Close()

	if f := readFrameOfType(t, sc, "slot-rebind"); f.OccupantPeerID != pass.ID {
		t.Fatalf("go-live did not replay the pre-live binding; slot-rebind occupant = %q, want %q", f.OccupantPeerID, pass.ID)
	}
}

// Deleting the live stream tears down its room too (codex P2/D-40): the FK cascade drops the
// session row, so the live room must be terminated or it (and its peers) would linger into the
// host's next stream. Covers both delete paths (app POST + API DELETE).
func TestSession_DeleteLiveStreamTearsDownRoom(t *testing.T) {
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct {
		name   string
		delete func(t *testing.T, base, streamID string, cookie *http.Cookie)
	}{
		{"app POST", func(t *testing.T, base, streamID string, cookie *http.Cookie) {
			req, _ := http.NewRequest(http.MethodPost, base+"/app/streams/"+streamID+"/delete", nil)
			req.AddCookie(cookie)
			resp, err := noRedirect.Do(req)
			if err != nil {
				t.Fatalf("delete POST: %v", err)
			}
			_ = resp.Body.Close()
		}},
		{"API DELETE", func(t *testing.T, base, streamID string, cookie *http.Cookie) {
			req, _ := http.NewRequest(http.MethodDelete, base+"/api/streams/"+streamID, nil)
			req.AddCookie(cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("delete API: %v", err)
			}
			_ = resp.Body.Close()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newWSHarness(t, wsHarnessOpts{})
			host, cookie := h.seedHost(t, "del-"+strings.ReplaceAll(tc.name, " ", ""), store.HostActive)
			stream := h.seedStream(t, host.ID)
			passRaw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
			if _, err := h.store.StartSession(ctx, stream.ID, host.ID); err != nil {
				t.Fatalf("StartSession: %v", err)
			}

			gc := h.dialOK(t, "pass="+passRaw, nil)
			defer gc.CloseNow()
			_ = wsReadFrame(t, gc)
			if h.hub.RoomIfLive(host.ID) == nil {
				t.Fatal("room should be live after the guest joined")
			}

			tc.delete(t, h.srv.URL, stream.ID, cookie)

			if f := readFrameOfType(t, gc, "terminate"); f.Reason != signaling.TerminateSessionEnded {
				t.Fatalf("terminate reason = %q, want %q", f.Reason, signaling.TerminateSessionEnded)
			}
			if h.hub.RoomIfLive(host.ID) != nil {
				t.Fatal("deleting the live stream must tear down its room")
			}
		})
	}
}

// Concurrent go-live / end-session for one host stay race-free and convergent (codex): the
// per-host binding lock serializes the DB mutation with the room teardown, so end can't clear the
// session in a gap before its room is removed. Run under -race; the system ends in a clean state.
func TestSession_ConcurrentGoLiveEndNoRace(t *testing.T) {
	ctx := context.Background()
	a := newAPIHarness(t)
	host, cookie := a.host(t, "session-race")
	x := a.createStream(t, cookie, "X")

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		path := "/app/streams/" + x + "/session/start"
		if i%2 == 1 {
			path = "/app/streams/" + x + "/session/end"
		}
		go func(path string) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, path, nil)
			req.AddCookie(cookie)
			a.h.ServeHTTP(httptest.NewRecorder(), req)
		}(path)
	}
	wg.Wait()

	// A final explicit end leaves the host with no active session — no corruption survived the storm.
	endReq := httptest.NewRequest(http.MethodPost, "/app/streams/"+x+"/session/end", nil)
	endReq.AddCookie(cookie)
	a.h.ServeHTTP(httptest.NewRecorder(), endReq)
	if _, err := a.store.ActiveSession(ctx, host.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after a concurrent start/end storm + final end, want no active session, got err=%v", err)
	}
}

// Deleting a NON-live stream whose guests already connected (pre-live: the room spawned before
// Go live) must evict those peers (codex P2): their passes are cascade-deleted, so the orphaned
// sockets would otherwise linger in the host-scoped room and carry into the next session. They get
// the terminal revoked teardown. Covers both delete paths.
func TestSession_DeletePreLiveStreamEvictsPeers(t *testing.T) {
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct {
		name   string
		delete func(t *testing.T, base, streamID string, cookie *http.Cookie)
	}{
		{"app POST", func(t *testing.T, base, streamID string, cookie *http.Cookie) {
			req, _ := http.NewRequest(http.MethodPost, base+"/app/streams/"+streamID+"/delete", nil)
			req.AddCookie(cookie)
			resp, err := noRedirect.Do(req)
			if err != nil {
				t.Fatalf("delete POST: %v", err)
			}
			_ = resp.Body.Close()
		}},
		{"API DELETE", func(t *testing.T, base, streamID string, cookie *http.Cookie) {
			req, _ := http.NewRequest(http.MethodDelete, base+"/api/streams/"+streamID, nil)
			req.AddCookie(cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("delete API: %v", err)
			}
			_ = resp.Body.Close()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWSHarness(t, wsHarnessOpts{})
			host, cookie := h.seedHost(t, "delprelive-"+strings.ReplaceAll(tc.name, " ", ""), store.HostActive)
			stream := h.seedStream(t, host.ID)
			passRaw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
			// NO StartSession: the guest connects pre-live, which still spawns the host-scoped room.
			gc := h.dialOK(t, "pass="+passRaw, nil)
			defer gc.CloseNow()
			_ = wsReadFrame(t, gc)
			if h.hub.RoomIfLive(host.ID) == nil {
				t.Fatal("pre-live guest should have spawned the room")
			}

			tc.delete(t, h.srv.URL, stream.ID, cookie)

			// The pre-live guest is evicted with the terminal revoked reason (its invite is gone).
			if f := readFrameOfType(t, gc, "terminate"); f.Reason != signaling.TerminateRevoked {
				t.Fatalf("evicted guest terminate reason = %q, want %q", f.Reason, signaling.TerminateRevoked)
			}
		})
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
