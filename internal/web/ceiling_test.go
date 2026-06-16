package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

// AC-8 / RF-2: PUT /api/streams/{id}/ceiling validates ownership, clamps to sane bounds, and
// persists streams.max_*. A foreign/unknown stream is 404; unauthenticated is 401.
func TestCeiling_PersistsAndClamps(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	streamID := a.createStream(t, alice, "Show")
	ctx := context.Background()

	set := func(cookie *http.Cookie, id, body string) (int, putCeilingResponse) {
		rec := a.req(t, http.MethodPut, "/api/streams/"+id+"/ceiling", body, cookie)
		var resp putCeilingResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
		}
		return rec.Code, resp
	}

	if code, _ := set(bob, streamID, `{"maxRes":480,"maxFps":24,"maxBitrateKbps":1500}`); code != http.StatusNotFound {
		t.Fatalf("foreign-host ceiling = %d, want 404", code)
	}
	if code, _ := set(alice, "no-such-stream", `{"maxRes":480,"maxFps":24,"maxBitrateKbps":1500}`); code != http.StatusNotFound {
		t.Fatalf("unknown stream = %d, want 404", code)
	}
	if code := a.req(t, http.MethodPut, "/api/streams/"+streamID+"/ceiling", `{"maxRes":480}`, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", code)
	}

	// Valid set → persisted; not live (no active session) so Live is false.
	code, resp := set(alice, streamID, `{"maxRes":480,"maxFps":24,"maxBitrateKbps":1500}`)
	if code != http.StatusOK || resp.MaxRes != 480 || resp.MaxFps != 24 || resp.MaxBitrateKbps != 1500 || resp.Live {
		t.Fatalf("set ceiling = %d %+v, want 200/480/24/1500/live=false", code, resp)
	}
	if s, _ := a.store.GetStream(ctx, streamID); s.MaxRes == nil || *s.MaxRes != 480 || s.MaxFPS == nil || *s.MaxFPS != 24 {
		t.Fatalf("ceiling did not persist: res=%v fps=%v", s.MaxRes, s.MaxFPS)
	}

	// A PARTIAL body changes only the named dimension and PRESERVES the others (codex/Bugbot): a body
	// with just maxRes must not reset fps/bitrate to their minimums.
	_, resp = set(alice, streamID, `{"maxRes":600}`)
	if resp.MaxRes != 600 || resp.MaxFps != 24 || resp.MaxBitrateKbps != 1500 {
		t.Fatalf("partial PUT = %d/%d/%d, want 600/24/1500 (others preserved)", resp.MaxRes, resp.MaxFps, resp.MaxBitrateKbps)
	}
	if s, _ := a.store.GetStream(ctx, streamID); *s.MaxFPS != 24 || *s.MaxBitrateKbps != 1500 {
		t.Fatalf("partial PUT clobbered the unset fields: fps=%d kbps=%d", *s.MaxFPS, *s.MaxBitrateKbps)
	}

	// Out-of-range values are clamped server-side (no 0/divide-by-zero, no absurd bitrate).
	_, resp = set(alice, streamID, `{"maxRes":0,"maxFps":999,"maxBitrateKbps":9999999}`)
	if resp.MaxRes != minMaxRes || resp.MaxFps != maxMaxFps || resp.MaxBitrateKbps != maxMaxBitrateKbps {
		t.Fatalf("clamp = %d/%d/%d, want %d/%d/%d", resp.MaxRes, resp.MaxFps, resp.MaxBitrateKbps, minMaxRes, maxMaxFps, maxMaxBitrateKbps)
	}
	if s, _ := a.store.GetStream(ctx, streamID); *s.MaxRes != minMaxRes {
		t.Fatalf("clamped value did not persist: %d", *s.MaxRes)
	}
}

// AC-8: GET /api/session/ceiling gives the greenroom the active session's stream id + ceiling to
// populate + target its control; 404 before Go live (the control stays hidden). Host-only.
func TestSessionCeiling_GET(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	streamID := a.createStream(t, alice, "Show")
	ctx := context.Background()

	// No live session → 404.
	if code := a.req(t, http.MethodGet, "/api/session/ceiling", "", alice).Code; code != http.StatusNotFound {
		t.Fatalf("no-session ceiling GET = %d, want 404", code)
	}
	// Unauthenticated → 401.
	if code := a.req(t, http.MethodGet, "/api/session/ceiling", "", nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d, want 401", code)
	}

	if _, err := a.store.StartSession(ctx, streamID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	rec := a.req(t, http.MethodGet, "/api/session/ceiling", "", alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("live ceiling GET = %d, want 200", rec.Code)
	}
	var resp sessionCeilingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StreamID != streamID || resp.MaxRes != 720 || resp.MaxFps != 30 || resp.MaxBitrateKbps != 2500 {
		t.Fatalf("session ceiling = %+v, want %s/720/30/2500", resp, streamID)
	}
}

// codex (upgrade path): a legacy stream with NULL ceiling columns (created before D-19) still
// resolves to the product default everywhere — the greenroom GET and the join delivery (passCeiling)
// both fall back to 720/30/2500 instead of returning zeros / no ceiling.
func TestCeiling_LegacyNullDefaults(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	streamID := a.createStream(t, alice, "Legacy")
	ctx := context.Background()

	// Simulate a pre-D-19 row: NULL out the ceiling columns.
	st, _ := a.store.GetStream(ctx, streamID)
	st.MaxRes, st.MaxFPS, st.MaxBitrateKbps = nil, nil, nil
	if err := a.store.UpdateStream(ctx, st); err != nil {
		t.Fatalf("UpdateStream: %v", err)
	}

	if _, err := a.store.StartSession(ctx, streamID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	rec := a.req(t, http.MethodGet, "/api/session/ceiling", "", alice)
	var resp sessionCeilingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.MaxRes != 720 || resp.MaxFps != 30 || resp.MaxBitrateKbps != 2500 {
		t.Fatalf("legacy NULL GET = %+v, want defaults 720/30/2500", resp)
	}

	// The join-delivery resolver falls back to defaults too.
	pass, err := a.store.CreatePass(ctx, store.CreatePassParams{StreamID: streamID, Role: store.RoleGuest, TokenHash: a.hasher.Hash("legacy"), Status: store.PassSent})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	wr := &wsResolver{store: a.store}
	mr, mf, mb, ok := wr.passCeiling(ctx, pass.ID)
	if !ok || mr != 720 || mf != 30 || mb != 2500 {
		t.Fatalf("legacy passCeiling = %d/%d/%d ok=%v, want defaults 720/30/2500", mr, mf, mb, ok)
	}
}

// AC-8 / T-8: a guest receives the stream's ceiling on JOIN (so it caps its program encoder the
// moment it publishes), and a live host adjustment re-broadcasts {t:ceiling} to the connected
// publisher. The full encoder-cap proof is the [BROWSER] test.
func TestCeiling_DeliveredOnJoinAndLiveAdjust(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "ceil", store.HostActive)
	stream := h.seedStream(t, host.ID) // CreateStream defaults the ceiling to 720/30/2500
	passRaw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	if _, err := h.store.StartSession(context.Background(), stream.ID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	// On join the guest gets the default ceiling.
	f := readFrameOfType(t, gc, "ceiling")
	if f.MaxRes != 720 || f.MaxFps != 30 || f.MaxBitrateKbps != 2500 {
		t.Fatalf("join ceiling = %d/%d/%d, want the default 720/30/2500", f.MaxRes, f.MaxFps, f.MaxBitrateKbps)
	}

	// Host lowers the ceiling live → the connected publisher gets a fresh {t:ceiling}.
	req, _ := http.NewRequest(http.MethodPut, h.srv.URL+"/api/streams/"+stream.ID+"/ceiling", strings.NewReader(`{"maxRes":360,"maxFps":20,"maxBitrateKbps":800}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("live ceiling PUT = %d, want 200", resp.StatusCode)
	}
	f = readFrameOfType(t, gc, "ceiling")
	if f.MaxRes != 360 || f.MaxFps != 20 || f.MaxBitrateKbps != 800 {
		t.Fatalf("live-adjust ceiling = %d/%d/%d, want 360/20/800", f.MaxRes, f.MaxFps, f.MaxBitrateKbps)
	}
}
