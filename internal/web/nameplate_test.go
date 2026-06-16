package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

// AC-7 / RF-2: PUT /api/passes/{id}/name validates ownership and persists the (capped) override to
// passes.name. A foreign host or unknown pass is 404 (ids can't be probed); unauthenticated is 401.
func TestNameOverride_PersistsAndCaps(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	streamID := a.createStream(t, alice, "Show")
	ctx := context.Background()
	invite := "Greta"
	pass, err := a.store.CreatePass(ctx, store.CreatePassParams{
		StreamID: streamID, Role: store.RoleGuest, Name: &invite, TokenHash: a.hasher.Hash("np-pass"), Status: store.PassSent,
	})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}

	set := func(cookie *http.Cookie, passID, body string) *struct {
		code int
		name string
		live bool
	} {
		rec := a.req(t, http.MethodPut, "/api/passes/"+passID+"/name", body, cookie)
		out := &struct {
			code int
			name string
			live bool
		}{code: rec.Code}
		if rec.Code == http.StatusOK {
			var resp struct {
				Name string `json:"name"`
				Live bool   `json:"live"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out.name, out.live = resp.Name, resp.Live
		}
		return out
	}

	if got := set(bob, pass.ID, `{"name":"Pwned"}`); got.code != http.StatusNotFound {
		t.Fatalf("foreign-host override = %d, want 404", got.code)
	}
	if got := set(alice, "no-such-pass", `{"name":"x"}`); got.code != http.StatusNotFound {
		t.Fatalf("unknown pass = %d, want 404", got.code)
	}
	if code := a.req(t, http.MethodPut, "/api/passes/"+pass.ID+"/name", `{"name":"x"}`, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated override = %d, want 401", code)
	}

	// Valid override → persisted, and not live (no active session) so Live is false.
	got := set(alice, pass.ID, `{"name":"Margaret"}`)
	if got.code != http.StatusOK || got.name != "Margaret" || got.live {
		t.Fatalf("override = %+v, want 200/Margaret/live=false", got)
	}
	if p, _ := a.store.GetPass(ctx, pass.ID); p.Name == nil || *p.Name != "Margaret" {
		t.Fatalf("override did not persist (%v)", p.Name)
	}

	// A hostile / over-long name is capped server-side (EN-15): control chars stripped, response
	// echoes the canonical capped form, and that is what is persisted.
	hostile := "Ma\nrg\x00aret" + strings.Repeat("x", 200)
	got = set(alice, pass.ID, `{"name":`+jsonString(hostile)+`}`)
	if got.code != http.StatusOK {
		t.Fatalf("hostile override = %d, want 200", got.code)
	}
	if strings.ContainsAny(got.name, "\n\x00") {
		t.Fatalf("capped name still carries control chars: %q", got.name)
	}
	if n := len([]rune(got.name)); n > 60 {
		t.Fatalf("capped name length %d exceeds the cap", n)
	}
	if p, _ := a.store.GetPass(ctx, pass.ID); p.Name == nil || *p.Name != got.name {
		t.Fatalf("persisted name %v != capped response %q", p.Name, got.name)
	}

	// Clearing the override (blank) drops the name to NULL.
	if got := set(alice, pass.ID, `{"name":"   "}`); got.code != http.StatusOK || got.name != "" {
		t.Fatalf("clear override = %+v, want 200/empty", got)
	}
	if p, _ := a.store.GetPass(ctx, pass.ID); p.Name != nil {
		t.Fatalf("clear did not NULL the name (%v)", p.Name)
	}
}

// AC-7 / T-7: a name override on a LIVE stream refreshes the OBS nameplate — the source receives a
// slot-rebind carrying the new name at the SAME epoch as the current bind (a name-only refresh, no
// media re-link / no epoch bump). The full escaped-render proof is the [BROWSER] tracer.
func TestNameOverride_LiveRefreshReachesSource(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "np-live", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	srcRaw, _ := h.seedCamSlot(t, host.ID, 1)

	if _, err := h.store.StartSession(context.Background(), stream.ID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	_ = wsReadFrame(t, gc) // guest roster — its join completed

	sc := h.dialOK(t, "src="+srcRaw, http.Header{"Origin": {"null"}})
	defer sc.CloseNow()
	if f := wsReadFrame(t, sc); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}

	put := func(path, body string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, h.srv.URL+path, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s = %d, want 200", path, resp.StatusCode)
		}
	}

	// Bind the guest to cam-1; capture the bind epoch.
	put("/api/passes/"+pass.ID+"/slot", `{"slot":"cam-1"}`)
	bind := readFrameOfType(t, sc, "slot-rebind")
	if bind.OccupantPeerID != pass.ID || bind.Epoch == nil {
		t.Fatalf("bind slot-rebind = occupant %q epoch %v, want the guest + an epoch", bind.OccupantPeerID, bind.Epoch)
	}
	bindEpoch := *bind.Epoch

	// Override the name → the source gets a same-epoch slot-rebind carrying the new name.
	put("/api/passes/"+pass.ID+"/name", `{"name":"Margaret"}`)
	refresh := readFrameOfType(t, sc, "slot-rebind")
	if refresh.Name != "Margaret" {
		t.Fatalf("refresh slot-rebind name = %q, want Margaret", refresh.Name)
	}
	if refresh.OccupantPeerID != pass.ID {
		t.Fatalf("refresh occupant = %q, want the same guest %q", refresh.OccupantPeerID, pass.ID)
	}
	if refresh.Epoch == nil || *refresh.Epoch != bindEpoch {
		t.Fatalf("refresh epoch = %v, want the SAME bind epoch %d (no media re-link)", refresh.Epoch, bindEpoch)
	}
	if p, _ := h.store.GetPass(context.Background(), pass.ID); p.Name == nil || *p.Name != "Margaret" {
		t.Fatalf("live override did not persist (%v)", p.Name)
	}
}

// jsonString encodes s as a JSON string literal (for building request bodies with control chars).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
