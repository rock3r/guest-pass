package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// seedGuestPass creates a guest pass with PII on a stream (for export/cascade tests).
func (a *apiHarness) seedGuestPass(t *testing.T, streamID, name, email, tokenHash string) {
	t.Helper()
	if _, err := a.store.CreatePass(context.Background(), store.CreatePassParams{
		StreamID: streamID, Name: &name, Email: &email, TokenHash: tokenHash,
	}); err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
}

func TestGDPR_RoutesRequireAuth(t *testing.T) {
	a := newAPIHarness(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/me/export"},
		{http.MethodPatch, "/api/me"},
		{http.MethodDelete, "/api/me"},
	} {
		if rec := a.req(t, tc.method, tc.path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without cookie = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

// Export returns the host PII surface as a JSON attachment, scoped to the host, and NEVER leaks a
// token hash (AC-3 / EN-5 / EN-8).
func TestGDPR_ExportHostScopedNoTokens(t *testing.T) {
	a := newAPIHarness(t)
	host, cookie := a.host(t, "exporter")
	streamID := a.createStream(t, cookie, "My Show")
	a.seedGuestPass(t, streamID, "Greta", "greta@example.com", "secret-pass-hash-AAAA")

	// Another host's data, which must NOT appear in this export.
	other, otherCookie := a.host(t, "stranger")
	otherStream := a.createStream(t, otherCookie, "Other Show")
	a.seedGuestPass(t, otherStream, "Otto", "otto@example.com", "secret-pass-hash-BBBB")
	_ = other

	rec := a.req(t, http.MethodGet, "/api/me/export", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d, body %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "guestpass-export.json") {
		t.Fatalf("Content-Disposition = %q, want an attachment download", cd)
	}
	body := rec.Body.String()

	// No token hash (the crown jewels) leaks into the export — neither the JSON key nor any value.
	if strings.Contains(body, "token_hash") || strings.Contains(body, "secret-pass-hash") {
		t.Fatalf("export leaked a token hash:\n%s", body)
	}
	// Another host's data must not appear (host-scoped, EN-8).
	if strings.Contains(body, "otto@example.com") || strings.Contains(body, "Other Show") {
		t.Fatalf("export leaked another host's data:\n%s", body)
	}

	var data exportData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if data.Account.Email != host.Email || data.Account.Name != host.Name {
		t.Fatalf("account = %+v, want this host's identity", data.Account)
	}
	if len(data.Streams) != 1 || data.Streams[0].Title != "My Show" {
		t.Fatalf("streams = %+v, want [My Show]", data.Streams)
	}
	if len(data.Guests) != 1 || data.Guests[0].Email != "greta@example.com" {
		t.Fatalf("guests = %+v, want [greta]", data.Guests)
	}
}

// Amend rectifies the host's display name; an empty name is rejected (AC-4).
func TestGDPR_AmendName(t *testing.T) {
	a := newAPIHarness(t)
	host, cookie := a.host(t, "amender")

	rec := a.req(t, http.MethodPatch, "/api/me", `{"name":"  Renamed Host  "}`, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("amend = %d, body %s", rec.Code, rec.Body.String())
	}
	got, _ := a.store.GetHost(context.Background(), host.ID)
	if got.Name != "Renamed Host" { // trimmed
		t.Fatalf("name = %q, want trimmed 'Renamed Host'", got.Name)
	}

	if rec := a.req(t, http.MethodPatch, "/api/me", `{"name":"   "}`, cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty-name amend = %d, want 400", rec.Code)
	}
}

// Delete is refused (409) while the host has a live session, and the account survives (D-M5-3/AC-5).
func TestGDPR_DeleteRefusedWhileLive(t *testing.T) {
	a := newAPIHarness(t)
	host, cookie := a.host(t, "livehost")
	streamID := a.createStream(t, cookie, "Live Show")
	if _, err := a.store.StartSession(context.Background(), streamID, host.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	rec := a.req(t, http.MethodDelete, "/api/me", "", cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete while live = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "end your live stream first") {
		t.Fatalf("delete-while-live body = %q", rec.Body.String())
	}
	if _, err := a.store.GetHost(context.Background(), host.ID); err != nil {
		t.Fatalf("host must still exist after a refused delete: %v", err)
	}
}

// Delete erases the account + all the host's data and clears the session cookie (AC-5).
func TestGDPR_DeleteErasesAccount(t *testing.T) {
	a := newAPIHarness(t)
	host, cookie := a.host(t, "leaver")
	streamID := a.createStream(t, cookie, "Doomed Show")
	a.seedGuestPass(t, streamID, "Guest", "g@example.com", "doomed-tok")

	rec := a.req(t, http.MethodDelete, "/api/me", "", cookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, body %s", rec.Code, rec.Body.String())
	}
	// Host + their data gone.
	if _, err := a.store.GetHost(context.Background(), host.ID); err == nil {
		t.Fatal("host still present after delete")
	}
	if streams, _ := a.store.ListStreamsByHost(context.Background(), host.ID); len(streams) != 0 {
		t.Fatalf("host's streams not cascaded: %d", len(streams))
	}
	// Session cookie cleared (logout).
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("delete must clear the session cookie")
	}
}

// The no-JS settings page exposes the functional GDPR controls (export link + amend/delete forms),
// not the M4 disabled stubs (AC-3..5).
func TestGDPR_SettingsPageHasFunctionalControls(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "settingshost")
	rec := a.req(t, http.MethodGet, "/app/settings", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/api/me/export"`,         // functional export download link
		`action="/app/settings/amend"`,  // amend form
		`action="/app/settings/delete"`, // delete form
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings page missing %q", want)
		}
	}
	if strings.Contains(body, "lands in a later release") || strings.Contains(body, "disabled") {
		t.Fatalf("settings page still shows the M4 disabled stubs:\n%s", body)
	}
}
