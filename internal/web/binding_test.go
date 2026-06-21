package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// seedGuestPass + a cam-1 slot for binding tests, returning the pass and the slot.
func (a *apiHarness) seedSlotAndPass(t *testing.T, hostID, streamID, tokenSeed string) (*store.Pass, *store.Slot) {
	t.Helper()
	ctx := context.Background()
	pass, err := a.store.CreatePass(ctx, store.CreatePassParams{
		StreamID: streamID, Role: store.RoleGuest, TokenHash: a.hasher.Hash("pass-" + tokenSeed), Status: store.PassSent,
	})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	idx := int64(1)
	slot, err := a.store.CreateSlot(ctx, store.CreateSlotParams{
		HostID: hostID, Kind: store.SlotCam, Idx: &idx, SourceTokenHash: a.hasher.Hash("slot-" + tokenSeed),
	})
	if err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	return pass, slot
}

// AC-6 / RF-2: PUT /api/passes/{id}/slot validates ownership, cam-only, single-occupant, and
// provisioning, persisting passes.slot_id on success.
func TestBinding_ValidationAndPersistence(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	streamID := a.createStream(t, alice, "Show")
	pass, slot := a.seedSlotAndPass(t, host.ID, streamID, "x")
	ctx := context.Background()

	bind := func(cookie *http.Cookie, passID, body string) int {
		return a.req(t, http.MethodPut, "/api/passes/"+passID+"/slot", body, cookie).Code
	}

	if code := bind(bob, pass.ID, `{"slot":"cam-1"}`); code != http.StatusNotFound {
		t.Fatalf("foreign-host bind = %d, want 404", code)
	}
	if code := bind(alice, "no-such-pass", `{"slot":"cam-1"}`); code != http.StatusNotFound {
		t.Fatalf("unknown pass = %d, want 404", code)
	}
	if code := bind(alice, pass.ID, `{"slot":"screen"}`); code != http.StatusBadRequest {
		t.Fatalf("non-cam slot = %d, want 400", code)
	}
	if code := bind(alice, pass.ID, `{"slot":"cam-99"}`); code != http.StatusBadRequest {
		t.Fatalf("out-of-range cam = %d, want 400", code)
	}
	if code := bind(alice, pass.ID, `{"slot":"cam-2"}`); code != http.StatusNotFound {
		t.Fatalf("unprovisioned slot = %d, want 404", code)
	}

	// Valid bind → persisted.
	if code := bind(alice, pass.ID, `{"slot":"cam-1"}`); code != http.StatusOK {
		t.Fatalf("valid bind = %d, want 200", code)
	}
	if got, _ := a.store.GetPass(ctx, pass.ID); got.SlotID == nil || *got.SlotID != slot.ID {
		t.Fatalf("bind did not persist slot_id (%v, want %s)", got.SlotID, slot.ID)
	}

	// Binding a SECOND guest to cam-1 swaps (the DoD "swap a slot occupant"): pass2 takes the
	// slot and the original occupant (pass) is displaced — its persistent binding cleared (RF-2:
	// still at most one occupant). Single occupant is preserved by displacement, not rejection.
	pass2, err := a.store.CreatePass(ctx, store.CreatePassParams{
		StreamID: streamID, Role: store.RoleGuest, TokenHash: a.hasher.Hash("pass-2"), Status: store.PassSent,
	})
	if err != nil {
		t.Fatalf("CreatePass 2: %v", err)
	}
	if code := bind(alice, pass2.ID, `{"slot":"cam-1"}`); code != http.StatusOK {
		t.Fatalf("swap bind = %d, want 200", code)
	}
	if got, _ := a.store.GetPass(ctx, pass2.ID); got.SlotID == nil || *got.SlotID != slot.ID {
		t.Fatalf("swap did not bind pass2 to cam-1 (%v)", got.SlotID)
	}
	if got, _ := a.store.GetPass(ctx, pass.ID); got.SlotID != nil {
		t.Fatalf("swap did not displace the original occupant (%v)", got.SlotID)
	}

	// Unassign pass2 → slot_id cleared.
	if code := bind(alice, pass2.ID, `{"slot":""}`); code != http.StatusOK {
		t.Fatalf("unassign = %d, want 200", code)
	}
	if got, _ := a.store.GetPass(ctx, pass2.ID); got.SlotID != nil {
		t.Fatalf("unassign did not clear slot_id (%v)", got.SlotID)
	}

	// Auth gate.
	if code := a.req(t, http.MethodPut, "/api/passes/"+pass.ID+"/slot", `{"slot":"cam-1"}`, nil).Code; code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bind = %d, want 401", code)
	}
}

// readFrameOfType reads frames until one matches typ (or fails), skipping interleaved frames.
func readFrameOfType(t *testing.T, c *websocket.Conn, typ string) signaling.Frame {
	t.Helper()
	for i := 0; i < 10; i++ {
		if f := wsReadFrame(t, c); f.T == typ {
			return f
		}
	}
	t.Fatalf("did not receive a %q frame", typ)
	return signaling.Frame{}
}

// AC-6 / T-6: PUT /api/passes/{id}/slot re-routes the LIVE /s/{slot} source — the OBS source
// receives a slot-rebind naming the new occupant with a bumped epoch (EN-3), with no OBS
// edit — and the binding persists. The full media-render proof is the [BROWSER] tracer.
func TestBinding_LiveRebindReachesSource(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "binder", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	srcRaw, slot := h.seedCamSlot(t, host.ID, 1)

	// The host is live for this stream, so the picker's live reroute is in-scope (EN-2/D-20).
	if _, err := h.store.StartSession(context.Background(), stream.ID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// The guest joins (so the rebind has an in-room occupant) and the OBS source joins cam-1.
	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	_ = wsReadFrame(t, gc) // the guest's roster, confirming its join completed

	sc := h.dialOK(t, "src="+srcRaw, http.Header{"Origin": {"null"}})
	defer sc.CloseNow()
	if f := wsReadFrame(t, sc); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}

	// Host binds the guest to cam-1 over the REST endpoint (the greenroom People control).
	req, _ := http.NewRequest(http.MethodPut, h.srv.URL+"/api/passes/"+pass.ID+"/slot", strings.NewReader(`{"slot":"cam-1"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bind = %d, want 200", resp.StatusCode)
	}

	// The source re-routes to the guest, epoch bumped (no OBS edit, EN-3).
	f := readFrameOfType(t, sc, "slot-rebind")
	if f.OccupantPeerID != pass.ID {
		t.Fatalf("slot-rebind occupant = %q, want the guest %q", f.OccupantPeerID, pass.ID)
	}
	if f.Epoch == nil || *f.Epoch < 1 {
		t.Fatalf("slot-rebind epoch = %v, want >= 1 (bumped)", f.Epoch)
	}
	if got, _ := h.store.GetPass(context.Background(), pass.ID); got.SlotID == nil || *got.SlotID != slot.ID {
		t.Fatal("live bind did not persist slot_id")
	}
}

// M5.5/AC-2 (DESIGN §6 guest-left): a DELIBERATE leave — the guest clicks "Leave the greenroom",
// sending {t:"leave"} — vacates the bound cam slot IMMEDIATELY, so the OBS source re-routes to empty
// at once, instead of the transient grace-retain a bare socket drop gets (D-40). Without the terminal
// vacate the source would hold the departed guest's frozen frame for the whole grace window (codex).
func TestBinding_DeliberateLeaveVacatesSlotImmediately(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "leaver", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	srcRaw, _ := h.seedCamSlot(t, host.ID, 1)
	if _, err := h.store.StartSession(context.Background(), stream.ID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	_ = wsReadFrame(t, gc) // the guest's roster, confirming its join completed

	sc := h.dialOK(t, "src="+srcRaw, http.Header{"Origin": {"null"}})
	defer sc.CloseNow()
	if f := wsReadFrame(t, sc); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}

	// Bind the guest live → the source renders the guest.
	req, _ := http.NewRequest(http.MethodPut, h.srv.URL+"/api/passes/"+pass.ID+"/slot", strings.NewReader(`{"slot":"cam-1"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = resp.Body.Close()
	if f := readFrameOfType(t, sc, "slot-rebind"); f.OccupantPeerID != pass.ID {
		t.Fatalf("slot-rebind occupant = %q, want the guest %q", f.OccupantPeerID, pass.ID)
	}

	// The guest deliberately leaves → the source must re-route to empty (slot-unbound) right away. If
	// the leave were treated like a transient drop, the slot would stay grace-bound and no unbound
	// would arrive within the read deadline (≪ the grace window) — failing this read.
	wsWriteFrame(t, gc, signaling.Frame{T: "leave"})
	if f := readFrameOfType(t, sc, "slot-unbound"); f.T != "slot-unbound" {
		t.Fatalf("after a deliberate leave, source frame = %q, want slot-unbound (immediate vacate)", f.T)
	}
}

// codex/AC-6 (D-40): a slot binding persisted BEFORE the guest connects (or surviving a
// reconnect) is replayed as a live rebind when the guest joins — so /s/{slot} re-routes to it
// without the host re-binding. The OBS source can connect first and still pick up the occupant.
func TestBinding_PersistedBindingReplaysOnJoin(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "replay", store.HostActive)
	stream := h.seedStream(t, host.ID)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	srcRaw, slot := h.seedCamSlot(t, host.ID, 1)

	// Persist the binding (no live room yet) — as if the host bound before the stream started.
	if err := h.store.AssignPassSlot(context.Background(), pass.ID, slot.ID); err != nil {
		t.Fatalf("AssignPassSlot: %v", err)
	}
	// The host goes live for THIS stream so the join-replay is in-scope (EN-2/D-20).
	if _, err := h.store.StartSession(context.Background(), stream.ID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// The OBS source connects FIRST: no occupant in the room yet → slot-unbound.
	sc := h.dialOK(t, "src="+srcRaw, http.Header{"Origin": {"null"}})
	defer sc.CloseNow()
	if f := wsReadFrame(t, sc); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}

	// The guest connects → its persisted binding replays → the source re-routes to it.
	gc := h.dialOK(t, "pass="+passRaw, nil)
	defer gc.CloseNow()
	if f := readFrameOfType(t, sc, "slot-rebind"); f.OccupantPeerID != pass.ID {
		t.Fatalf("replayed slot-rebind occupant = %q, want the guest %q", f.OccupantPeerID, pass.ID)
	}
}

// The greenroom seeds its picker from GET /api/passes/slot-bindings (codex): the host's persisted
// pass→cam-slot bindings, so a pre-live (DB-only) selection survives a refresh. Host-only.
func TestBinding_ListSlotBindings(t *testing.T) {
	ctx := context.Background()
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "list-bind", store.HostActive)
	stream := h.seedStream(t, host.ID)
	_, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	_, slot := h.seedCamSlot(t, host.ID, 1)
	if err := h.store.AssignPassSlot(ctx, pass.ID, slot.ID); err != nil {
		t.Fatalf("AssignPassSlot: %v", err)
	}
	// A pass bound to cam-2 but PAST its expires_at deadline (status still "sent") must be excluded
	// from the seed — passJoinable/BoundCamPassesForStream/Sources all treat it as retired (codex).
	past := time.Now().Add(-time.Hour).Unix()
	_, expired := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, &past)
	_, cam2 := h.seedCamSlot(t, host.ID, 2)
	if err := h.store.AssignPassSlot(ctx, expired.ID, cam2.ID); err != nil {
		t.Fatalf("AssignPassSlot(expired): %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/api/passes/slot-bindings", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var m map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m[pass.ID] != "cam-1" {
		t.Fatalf("bindings = %v, want %s → cam-1", m, pass.ID)
	}
	if _, ok := m[expired.ID]; ok {
		t.Fatalf("a deadline-expired pass must not be seeded, got %v", m)
	}
}

// Binding a RETIRED (revoked/expired) pass is rejected before AssignPassSlot (codex): it would
// otherwise displace the slot's live occupant and route OBS to a guest who can't connect.
func TestBinding_RejectsRetiredPass(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "retired-bind", store.HostActive)
	stream := h.seedStream(t, host.ID)
	_, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassRevoked, nil)
	_, _ = h.seedCamSlot(t, host.ID, 1)

	req, _ := http.NewRequest(http.MethodPut, h.srv.URL+"/api/passes/"+pass.ID+"/slot", strings.NewReader(`{"slot":"cam-1"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("binding a revoked pass = %d, want 409", resp.StatusCode)
	}
	// The slot must NOT have been assigned to the retired pass.
	if got, _ := h.store.GetPass(context.Background(), pass.ID); got.SlotID != nil {
		t.Fatalf("revoked pass was bound to %v despite rejection", got.SlotID)
	}
}

// The join-replay is gated on the active session's stream (codex P1): guestBoundSlot — the
// resolver the /ws join calls to decide what to replay — returns a label ONLY when the binding's
// stream is the host's LIVE one. A guest of an upcoming (non-live) stream whose pass carries a
// preassigned cam slot must resolve to "" (no replay), so opening their link mid-show can't
// hijack the host-global on-air pool. Testing the resolver directly keeps the gate deterministic.
func TestBinding_ReplayGatedByActiveSession(t *testing.T) {
	ctx := context.Background()
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "replay-gate", store.HostActive)
	live := h.seedStream(t, host.ID)  // the stream the host actually goes live for
	other := h.seedStream(t, host.ID) // an upcoming, NON-live stream
	_, otherPass := h.seedPass(t, other.ID, store.RoleGuest, store.PassSent, nil)
	_, slot := h.seedCamSlot(t, host.ID, 1)

	if err := h.store.AssignPassSlot(ctx, otherPass.ID, slot.ID); err != nil {
		t.Fatalf("AssignPassSlot: %v", err)
	}
	wr := &wsResolver{store: h.store}

	// No live session yet → nothing replays.
	if got := wr.guestBoundSlot(ctx, otherPass.ID, host.ID); got != "" {
		t.Fatalf("no live session: guestBoundSlot = %q, want \"\"", got)
	}
	// Host goes live for a DIFFERENT stream → the non-live-stream guest still must not replay.
	if _, err := h.store.StartSession(ctx, live.ID, host.ID); err != nil {
		t.Fatalf("StartSession(live): %v", err)
	}
	if got := wr.guestBoundSlot(ctx, otherPass.ID, host.ID); got != "" {
		t.Fatalf("non-live stream guest replayed cam-1 (%q) — must be gated out", got)
	}
	// Switch the live session to the guest's stream → now the binding is in-scope and replays.
	if err := h.store.EndActiveSession(ctx, host.ID); err != nil {
		t.Fatalf("EndActiveSession: %v", err)
	}
	if _, err := h.store.StartSession(ctx, other.ID, host.ID); err != nil {
		t.Fatalf("StartSession(other): %v", err)
	}
	if got := wr.guestBoundSlot(ctx, otherPass.ID, host.ID); got != "cam-1" {
		t.Fatalf("guest of the live stream should replay cam-1, got %q", got)
	}
}

// The greenroom picker's LIVE reroute is gated the same way as the replay (codex P1): streamIsLive
// — the predicate putPassSlot consults before touching the host-global room — is true only for the
// host's active session's stream. Binding an upcoming (non-live) stream's guest persists the DB
// binding but must be DB-only, so it can't vacate/hijack the on-air slot pool.
func TestBinding_PickerRerouteGatedByActiveSession(t *testing.T) {
	ctx := context.Background()
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "live-gate", store.HostActive)
	a := h.seedStream(t, host.ID)
	b := h.seedStream(t, host.ID)
	api := &apiServer{store: h.store}

	if api.streamIsLive(ctx, host.ID, a.ID) {
		t.Fatal("no active session: streamIsLive must be false (no live reroute)")
	}
	if _, err := h.store.StartSession(ctx, a.ID, host.ID); err != nil {
		t.Fatalf("StartSession(a): %v", err)
	}
	if !api.streamIsLive(ctx, host.ID, a.ID) {
		t.Fatal("stream A is live: streamIsLive must be true")
	}
	if api.streamIsLive(ctx, host.ID, b.ID) {
		t.Fatal("stream B is not live: a bind for B must be DB-only, not reroute the live room")
	}
}

// cursor (HIGH): binding an OFFLINE guest onto an OCCUPIED slot must DISPLACE the live
// occupant — the slot falls to placeholder (slot-unbound), it does NOT keep routing OBS to
// the displaced guest while the DB names the offline one.
func TestBinding_OfflineSwapVacatesSlot(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "offswap", store.HostActive)
	stream := h.seedStream(t, host.ID)
	aRaw, aPass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	_, bPass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil) // B never connects
	srcRaw, slot := h.seedCamSlot(t, host.ID, 1)

	// Host live for this stream so the picker's live reroute fires (EN-2/D-20).
	if _, err := h.store.StartSession(context.Background(), stream.ID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	put := func(passID, slotLabel string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, h.srv.URL+"/api/passes/"+passID+"/slot", strings.NewReader(`{"slot":"`+slotLabel+`"}`))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("put %s→%s = %d", passID, slotLabel, resp.StatusCode)
		}
	}

	// A connects; the OBS source connects; bind A to cam-1 (source renders A).
	ac := h.dialOK(t, "pass="+aRaw, nil)
	defer ac.CloseNow()
	_ = wsReadFrame(t, ac)
	sc := h.dialOK(t, "src="+srcRaw, http.Header{"Origin": {"null"}})
	defer sc.CloseNow()
	if f := wsReadFrame(t, sc); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}
	put(aPass.ID, "cam-1")
	if f := readFrameOfType(t, sc, "slot-rebind"); f.OccupantPeerID != aPass.ID {
		t.Fatalf("cam-1 not bound to A; got %q", f.OccupantPeerID)
	}

	// Reassign cam-1 to OFFLINE B: the source must go to placeholder (slot-unbound), NOT stay on A.
	put(bPass.ID, "cam-1")
	if f := readFrameOfType(t, sc, "slot-unbound"); f.Epoch == nil {
		t.Fatalf("source did not vacate to placeholder on the offline swap; got %+v", f)
	}
	// DB: B is bound, A is displaced.
	if got, _ := h.store.GetPass(context.Background(), bPass.ID); got.SlotID == nil || *got.SlotID != slot.ID {
		t.Fatal("offline B was not bound in the DB")
	}
	if got, _ := h.store.GetPass(context.Background(), aPass.ID); got.SlotID != nil {
		t.Fatalf("A was not displaced by the swap (%v)", got.SlotID)
	}
}

// The per-host binding lock keeps concurrent same-pass PUTs race-free and convergent: many
// overlapping bind/unassign requests complete with a consistent final binding (cam-1 or none),
// never a data race or a garbage slot_id (codex concurrency class). Run under -race.
func TestBinding_ConcurrentSamePassNoRace(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	streamID := a.createStream(t, alice, "Show")
	pass, slot := a.seedSlotAndPass(t, host.ID, streamID, "conc")

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		body := `{"slot":"cam-1"}`
		if i%2 == 1 {
			body = `{"slot":""}`
		}
		go func(b string) {
			defer wg.Done()
			if code := a.req(t, http.MethodPut, "/api/passes/"+pass.ID+"/slot", b, alice).Code; code != http.StatusOK {
				t.Errorf("concurrent PUT = %d, want 200", code)
			}
		}(body)
	}
	wg.Wait()

	got, err := a.store.GetPass(context.Background(), pass.ID)
	if err != nil {
		t.Fatalf("GetPass: %v", err)
	}
	if got.SlotID != nil && *got.SlotID != slot.ID {
		t.Fatalf("final slot_id = %v, want cam-1's id or nil (consistent)", got.SlotID)
	}
}
