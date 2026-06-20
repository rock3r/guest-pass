package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// decodeLive pulls the {"live":bool} body the session-status poll endpoint returns.
func decodeLive(t *testing.T, body []byte) bool {
	t.Helper()
	var v struct {
		Live bool `json:"live"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode session status %q: %v", body, err)
	}
	return v.Live
}

// M5.5: GET /api/streams/{id}/session reports whether THIS stream is the host's currently-live
// session — the read-only liveness the no-JS stream-detail page polls so its "● Live" pill stops
// going stale when the session is force-ended out from under the page (admin D-27 cascade, idle
// reaper, end-from-another-tab).
func TestSessionStatus_ReflectsLiveState(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")
	ctx := context.Background()

	// Pre-live: not the live session.
	rec := a.req(t, http.MethodGet, "/api/streams/"+id+"/session", "", alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("status (pre-live) = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if decodeLive(t, rec.Body.Bytes()) {
		t.Fatalf("pre-live status reported live=true")
	}

	// Go live → reported live.
	if _, err := a.store.StartSession(ctx, id, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	rec = a.req(t, http.MethodGet, "/api/streams/"+id+"/session", "", alice)
	if rec.Code != http.StatusOK || !decodeLive(t, rec.Body.Bytes()) {
		t.Fatalf("live status = %d live=%v, want 200 live=true", rec.Code, rec.Body.String())
	}

	// End the session (mimics the admin force-end's DB effect) → no longer live.
	if err := a.store.EndActiveSession(ctx, host.ID); err != nil {
		t.Fatalf("EndActiveSession: %v", err)
	}
	rec = a.req(t, http.MethodGet, "/api/streams/"+id+"/session", "", alice)
	if rec.Code != http.StatusOK || decodeLive(t, rec.Body.Bytes()) {
		t.Fatalf("ended status = %d body=%s, want 200 live=false", rec.Code, rec.Body.String())
	}
}

// EN-6 / RF-2: the poll endpoint is host-only and same-host — a foreign/unknown stream is 404 (ids
// can't be probed), an unauthenticated request is 401.
func TestSessionStatus_AuthAndOwnership(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	id := a.createStream(t, alice, "Alice Only")

	if rec := a.req(t, http.MethodGet, "/api/streams/"+id+"/session", "", bob); rec.Code != http.StatusNotFound {
		t.Fatalf("bob GET alice's session status = %d, want 404", rec.Code)
	}
	if rec := a.req(t, http.MethodGet, "/api/streams/"+id+"/session", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon GET session status = %d, want 401", rec.Code)
	}
}
