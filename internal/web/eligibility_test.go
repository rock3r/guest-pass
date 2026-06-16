package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// AC-9 / RF-2: PATCH /api/passes/{id} validates ownership and persists can_screen. A foreign/unknown
// pass is 404; unauthenticated is 401; a body with no canScreen is 400 (so a malformed PATCH can't
// silently revoke).
func TestEligibility_PersistsAndAuth(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	streamID := a.createStream(t, alice, "Show")
	ctx := context.Background()
	pass, err := a.store.CreatePass(ctx, store.CreatePassParams{StreamID: streamID, Role: store.RoleGuest, TokenHash: a.hasher.Hash("elig"), Status: store.PassSent})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}

	patch := func(cookie *http.Cookie, id, body string) (int, patchPassResponse) {
		rec := a.req(t, http.MethodPatch, "/api/passes/"+id, body, cookie)
		var resp patchPassResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
		}
		return rec.Code, resp
	}

	if code, _ := patch(bob, pass.ID, `{"canScreen":true}`); code != http.StatusNotFound {
		t.Fatalf("foreign-host PATCH = %d, want 404", code)
	}
	if code, _ := patch(alice, "no-such-pass", `{"canScreen":true}`); code != http.StatusNotFound {
		t.Fatalf("unknown pass = %d, want 404", code)
	}
	if code := a.req(t, http.MethodPatch, "/api/passes/"+pass.ID, `{"canScreen":true}`, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", code)
	}
	if code, _ := patch(alice, pass.ID, `{}`); code != http.StatusBadRequest {
		t.Fatalf("missing canScreen = %d, want 400", code)
	}

	// Grant → persists; not live (no session) so Live is false.
	code, resp := patch(alice, pass.ID, `{"canScreen":true}`)
	if code != http.StatusOK || !resp.CanScreen || resp.Live {
		t.Fatalf("grant = %d %+v, want 200/true/live=false", code, resp)
	}
	if p, _ := a.store.GetPass(ctx, pass.ID); !p.CanScreen {
		t.Fatal("grant did not persist can_screen")
	}
	// Revoke → persists false.
	if _, resp := patch(alice, pass.ID, `{"canScreen":false}`); resp.CanScreen {
		t.Fatal("revoke response still eligible")
	}
	if p, _ := a.store.GetPass(ctx, pass.ID); p.CanScreen {
		t.Fatal("revoke did not persist can_screen")
	}
}

// AC-9 / T-9: a guest's eligibility is SEEDED into the roster on join (its share affordance reflects
// passes.can_screen), and a live revoke re-projects it — the guest's own roster loses canScreen and
// gains the force-no-share lock. The full guest-UI proof is the [BROWSER] test.
func TestEligibility_JoinSeedAndLiveRevoke(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "elig-live", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	ctx := context.Background()
	if err := h.store.SetPassCanScreen(ctx, pass.ID, true); err != nil { // eligible before joining
		t.Fatalf("SetPassCanScreen: %v", err)
	}
	if _, err := h.store.StartSession(ctx, stream.ID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()

	// The join seed gives the guest's own roster entry canScreen.
	if e := awaitSelf(t, gc, pass.ID, func(e signaling.RosterEntry) bool { return e.CanScreen }); !e.CanScreen {
		t.Fatal("join seed did not give the guest its eligibility")
	}

	// Host revokes live → the guest loses eligibility and gains the force-no-share lock.
	req, _ := http.NewRequest(http.MethodPatch, h.srv.URL+"/api/passes/"+pass.ID, strings.NewReader(`{"canScreen":false}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke PATCH = %d, want 200", resp.StatusCode)
	}

	e := awaitSelf(t, gc, pass.ID, func(e signaling.RosterEntry) bool { return !e.CanScreen })
	if e.CanScreen {
		t.Fatal("revoke did not clear the guest's eligibility")
	}
	hasShare := false
	for _, l := range e.Locks {
		if l.Kind == "share" {
			hasShare = true
		}
	}
	if !hasShare {
		t.Fatalf("revoke did not apply the force-no-share lock; locks=%v", e.Locks)
	}
}

// awaitSelf reads roster frames until the recipient's own entry (id == self) satisfies cond, or
// fails. Skips interleaved non-roster frames.
func awaitSelf(t *testing.T, c *websocket.Conn, self string, cond func(signaling.RosterEntry) bool) signaling.RosterEntry {
	t.Helper()
	for i := 0; i < 20; i++ {
		f := wsReadFrame(t, c)
		if f.T != "roster" {
			continue
		}
		for _, e := range f.Peers {
			if e.ID == self && cond(e) {
				return e
			}
		}
	}
	t.Fatalf("no roster self entry satisfying the condition for %q", self)
	return signaling.RosterEntry{}
}
