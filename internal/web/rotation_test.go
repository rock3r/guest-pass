package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

// AC-5 / T-5: regenerating one slot rotates its source token — the old OBS URL stops
// resolving, a fresh URL is revealed once (POST-redirect-GET, no token in the URL), and the
// pool is otherwise untouched.
func TestApp_RegenerateSlotRotatesToken(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")

	// Provision the pool; capture cam-1's original (revealed) token.
	body := a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", alice).Body.String()
	oldToken := extractSlotToken(t, body, "cam-1")
	cam1 := a.slotByLabel(t, host.ID, store.SlotCam, ptrI64(1))

	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/sources/slots/"+cam1.ID+"/regenerate", nil, alice)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("regenerate = %d, want 303; body %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/app/streams/"+id+"/sources?reveal=") {
		t.Fatalf("regenerate Location = %q, want a reveal of the sources tab", loc)
	}
	if strings.Contains(loc, "token=") || strings.Contains(loc, oldToken) {
		t.Fatalf("regenerate redirect URL leaked a token: %q", loc)
	}

	// Old token no longer resolves; the slot still exists.
	if _, err := a.store.GetSlotBySourceTokenHash(context.Background(), a.hasher.Hash(oldToken)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old slot token still resolves after regenerate: %v", err)
	}

	// Following the reveal shows a NEW cam-1 URL whose token resolves to the same slot.
	reveal := a.req(t, http.MethodGet, loc, "", alice).Body.String()
	newToken := extractSlotToken(t, reveal, "cam-1")
	if newToken == oldToken {
		t.Fatal("regenerate did not rotate the token")
	}
	got, err := a.store.GetSlotBySourceTokenHash(context.Background(), a.hasher.Hash(newToken))
	if err != nil || got.ID != cam1.ID {
		t.Fatalf("new token does not resolve to cam-1: %v", err)
	}
	// Only cam-1 was rotated: the count is unchanged.
	if slots, _ := a.store.ListSlotsByHost(context.Background(), host.ID); len(slots) != 9 {
		t.Fatalf("regenerate changed the pool size to %d", len(slots))
	}
}

// AC-5 / T-5: rotate-all rotates EVERY slot's token at once (the "my URLs leaked" panic
// button) and reveals all the fresh URLs.
func TestApp_RegenerateAllRotatesEverySlot(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")

	body := a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", alice).Body.String()
	oldTokens := map[string]string{}
	for _, label := range []string{"cam-1", "cam-4", "cam-8", "screen"} {
		oldTokens[label] = extractSlotToken(t, body, label)
	}

	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/sources/regenerate-all", nil, alice)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("regenerate-all = %d, want 303", rec.Code)
	}
	reveal := a.req(t, http.MethodGet, rec.Header().Get("Location"), "", alice).Body.String()

	for label, old := range oldTokens {
		// Old token invalidated.
		if _, err := a.store.GetSlotBySourceTokenHash(context.Background(), a.hasher.Hash(old)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s old token still resolves after rotate-all", label)
		}
		// New token revealed + resolves.
		nw := extractSlotToken(t, reveal, label)
		if nw == old {
			t.Fatalf("%s token was not rotated", label)
		}
		if _, err := a.store.GetSlotBySourceTokenHash(context.Background(), a.hasher.Hash(nw)); err != nil {
			t.Fatalf("%s new token does not resolve: %v", label, err)
		}
	}
}

// EN-6: a foreign host cannot regenerate another host's slots, and an unknown slot id is 404.
func TestApp_RegenerateOwnershipIsolation(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	id := a.createStream(t, alice, "Alice Only")
	a.req(t, http.MethodGet, "/app/streams/"+id+"/sources", "", alice) // provision alice's pool
	cam1 := a.slotByLabel(t, host.ID, store.SlotCam, ptrI64(1))

	// Bob has his own stream but tries to rotate alice's slot via her stream id (foreign stream → 404).
	if rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/sources/slots/"+cam1.ID+"/regenerate", nil, bob); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign-stream regenerate = %d, want 404", rec.Code)
	}
	// An unknown slot id under alice's own stream → 404.
	if rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/sources/slots/no-such-slot/regenerate", nil, alice); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown-slot regenerate = %d, want 404", rec.Code)
	}
	// The token is unchanged after the rejected attempts (still resolves).
	if _, err := a.store.GetSlot(context.Background(), cam1.ID); err != nil {
		t.Fatalf("alice's slot was disturbed: %v", err)
	}
}
