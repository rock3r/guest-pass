package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

// extractSlotToken pulls the raw token out of a revealed `/s/{label}?token=…` OBS URL in
// the rendered page (the token runs until the first quote/space/delimiter).
func extractSlotToken(t *testing.T, body, label string) string {
	t.Helper()
	marker := "/s/" + label + "?token="
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no %s OBS URL revealed in body", label)
	}
	rest := body[i+len(marker):]
	end := strings.IndexAny(rest, "\"'<> \n\r\t&")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func (a *apiHarness) slotByLabel(t *testing.T, hostID, kind string, idx *int64) *store.Slot {
	t.Helper()
	slots, err := a.store.ListSlotsByHost(context.Background(), hostID)
	if err != nil {
		t.Fatalf("ListSlotsByHost: %v", err)
	}
	for _, sl := range slots {
		if sl.Kind != kind {
			continue
		}
		if idx == nil && sl.Idx == nil {
			return sl
		}
		if idx != nil && sl.Idx != nil && *sl.Idx == *idx {
			return sl
		}
	}
	t.Fatalf("no slot kind=%s idx=%v for host", kind, idx)
	return nil
}

// AC-4: opening the Sources tab idempotently provisions the host's slot pool (cam 1–8 +
// screenshare) and reveals each slot's permanent OBS URL ONCE (the token is stored hashed,
// EN-5, so it can only be surfaced at mint). A second open does not duplicate the pool nor
// re-reveal the tokens.
func TestApp_SourcesProvisionsPoolAndRevealsOnce(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")

	rec := a.req(t, http.MethodGet, "/app/streams/"+a.createStream(t, alice, "Show")+"/sources", "", alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET sources = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	slots, err := a.store.ListSlotsByHost(context.Background(), host.ID)
	if err != nil {
		t.Fatalf("ListSlotsByHost: %v", err)
	}
	if len(slots) != 9 {
		t.Fatalf("provisioned %d slots, want 9 (cam 1–8 + screenshare)", len(slots))
	}
	// Every slot's URL is revealed, and the revealed token actually resolves the slot.
	for _, label := range []string{"cam-1", "cam-8", "screen"} {
		raw := extractSlotToken(t, body, label)
		if raw == "" {
			t.Fatalf("empty token revealed for %s", label)
		}
		if _, err := a.store.GetSlotBySourceTokenHash(context.Background(), a.hasher.Hash(raw)); err != nil {
			t.Fatalf("revealed %s token does not resolve to a slot: %v", label, err)
		}
	}

	// Re-open: still 9 slots (idempotent), and the tokens are NOT re-revealed.
	id2 := a.createStream(t, alice, "Other")
	rec2 := a.req(t, http.MethodGet, "/app/streams/"+id2+"/sources", "", alice)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second sources GET = %d", rec2.Code)
	}
	if slots2, _ := a.store.ListSlotsByHost(context.Background(), host.ID); len(slots2) != 9 {
		t.Fatalf("re-open changed the pool to %d slots (not idempotent)", len(slots2))
	}
	if strings.Contains(rec2.Body.String(), "?token=") {
		t.Fatalf("re-opening the Sources tab re-revealed a slot token (reveal must be once)")
	}
}

// AC-4: each card shows the current occupant — the pass of THIS stream bound to the slot.
func TestApp_SourcesShowsOccupant(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")

	// Provision the pool, then bind a named guest to cam-1.
	a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", alice)
	cam1 := a.slotByLabel(t, host.ID, store.SlotCam, ptrI64(1))
	name := "Robin Occupant"
	pass, err := a.store.CreatePass(context.Background(), store.CreatePassParams{
		StreamID: id, Name: &name, TokenHash: "occ-hash", Status: store.PassSent,
	})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	if err := a.store.AssignPassSlot(context.Background(), pass.ID, cam1.ID); err != nil {
		t.Fatalf("AssignPassSlot: %v", err)
	}

	rec := a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", alice)
	if !strings.Contains(rec.Body.String(), "Robin Occupant") {
		t.Fatalf("cam-1 card does not show the bound occupant; body:\n%s", rec.Body.String())
	}
}

// A pass past its expiry deadline (status not yet swept) is no longer shown as the slot's
// occupant — the Sources tab uses the same deadline check as the Invites display (codex/
// Bugbot, M4 PR-4), so the two tabs stay consistent.
func TestApp_SourcesHidesDeadlineExpiredOccupant(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")
	a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", alice) // provision
	cam1 := a.slotByLabel(t, host.ID, store.SlotCam, ptrI64(1))

	name := "Lapsed Guest"
	past := int64(1) // 1970 — long past
	pass, err := a.store.CreatePass(context.Background(), store.CreatePassParams{
		StreamID: id, Name: &name, TokenHash: "exp-hash", Status: store.PassSent, ExpiresAt: &past,
	})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	if err := a.store.AssignPassSlot(context.Background(), pass.ID, cam1.ID); err != nil {
		t.Fatalf("AssignPassSlot: %v", err)
	}

	body := a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", alice).Body.String()
	if strings.Contains(body, "Lapsed Guest") {
		t.Fatalf("Sources tab shows a deadline-expired pass as occupant")
	}
}

// AC-4 / EN-26: the Sources tab is strictly read-only — no binding/nameplate/quality
// controls; each card links to the greenroom People controls instead.
func TestApp_SourcesHasNoEditableControls(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")

	body := a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", alice).Body.String()
	for _, forbidden := range []string{`name="slot_id"`, `name="nameplate"`, `name="max_res"`, `name="max_fps"`, `name="max_bitrate_kbps"`, `name="can_screen"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Sources tab exposes an editable control %s (EN-26)", forbidden)
		}
	}
	if !strings.Contains(body, "/greenroom") {
		t.Fatalf("Sources tab does not link to the greenroom People controls (EN-26)")
	}
}

// EN-6 isolation: a foreign host cannot view another host's Sources tab.
func TestApp_SourcesForeignStream404(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	id := a.createStream(t, alice, "Alice Only")
	if rec := a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", bob); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign sources GET = %d, want 404", rec.Code)
	}
}

func ptrI64(v int64) *int64 { return &v }
