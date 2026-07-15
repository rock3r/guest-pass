package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// formPost issues a urlencoded form POST with the host cookie (the no-JS settings forms).
func (a *apiHarness) formPost(t *testing.T, target, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

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
	if err := a.store.SetHostPreferences(context.Background(), store.HostPreferences{
		HostID: host.ID, Timezone: "Europe/Rome", YouTubeChannel: "guestpasslive", DefaultChannelPlatform: "youtube",
		MaxRes: 1080, MaxFPS: 60, MaxBitrateKbps: 4500, CustomTURNEnabled: true, CustomTURNURL: "turns:turn.example.test:5349", CustomTURNSecretEncrypted: "must-not-export",
	}); err != nil {
		t.Fatalf("SetHostPreferences: %v", err)
	}
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
	if strings.Contains(body, "custom_turn_secret_encrypted") || strings.Contains(body, "must-not-export") {
		t.Fatalf("export leaked the TURN shared secret:\n%s", body)
	}
	if !strings.Contains(body, `"preferences"`) || !strings.Contains(body, "Europe/Rome") || !strings.Contains(body, "turns:turn.example.test:5349") {
		t.Fatalf("export omitted non-secret host preferences:\n%s", body)
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

// Self-service delete is refused (409) when the host is the only active admin — erasing it would
// strand the instance with no admin (AC-9). Once a second active admin exists the delete proceeds,
// so erasure is deferred, not denied. The settings form redirects with ?error=last-admin.
func TestGDPR_DeleteRefusedWhenLastAdmin(t *testing.T) {
	a := newAPIHarness(t)
	admin, adminCookie := a.adminHost(t, "sole-admin")

	rec := a.req(t, http.MethodDelete, "/api/me", "", adminCookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete as sole admin = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "promote another admin") {
		t.Fatalf("sole-admin delete body = %q", rec.Body.String())
	}
	if _, err := a.store.GetHost(context.Background(), admin.ID); err != nil {
		t.Fatalf("admin must still exist after a refused delete: %v", err)
	}

	// The no-JS settings form takes the same path → PRG with ?error=last-admin.
	form := a.formPost(t, "/app/settings/delete", "confirm=1", adminCookie)
	if loc := form.Header().Get("Location"); loc != "/app/settings?error=last-admin" {
		t.Fatalf("settings delete (sole admin) loc=%q, want /app/settings?error=last-admin", loc)
	}

	// Promote a second active admin; now the first admin may erase themselves.
	other := a.hostInState(t, "other-admin", store.HostActive)
	if err := a.store.SetHostAdmin(context.Background(), other.ID, true); err != nil {
		t.Fatalf("SetHostAdmin(other): %v", err)
	}
	if rec := a.req(t, http.MethodDelete, "/api/me", "", adminCookie); rec.Code != http.StatusNoContent {
		t.Fatalf("delete with a second admin present = %d, want 204", rec.Code)
	}
	if _, err := a.store.GetHost(context.Background(), admin.ID); err == nil {
		t.Fatal("admin should be erased once a second admin exists")
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

// The settings delete form enforces the confirmation field SERVER-SIDE (codex): a POST missing
// confirm=1 must NOT erase the account (the `required` checkbox is only a browser affordance).
func TestGDPR_SettingsDeleteRequiresConfirmServerSide(t *testing.T) {
	a := newAPIHarness(t)
	host, cookie := a.host(t, "noconfirm")

	rec := a.formPost(t, "/app/settings/delete", "", cookie) // no confirm field
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=confirm") {
		t.Fatalf("unconfirmed delete = %d loc=%q, want 303 → ?error=confirm", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := a.store.GetHost(context.Background(), host.ID); err != nil {
		t.Fatalf("host must survive a delete POST missing confirmation: %v", err)
	}

	// With confirm=1 it proceeds and erases.
	rec = a.formPost(t, "/app/settings/delete", "confirm=1", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirmed delete = %d, want 303", rec.Code)
	}
	if _, err := a.store.GetHost(context.Background(), host.ID); err == nil {
		t.Fatal("confirmed delete should erase the host")
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
	// The GDPR self-service block must be live, not a disabled placeholder. (The M5.6 design adds
	// deliberately-stubbed "coming soon" cards for unrelated future features — scheduling defaults,
	// channels, video, TURN — which DO carry disabled inputs; those are owner-approved stubs and are
	// not what this test guards. So we scope the check to the GDPR self-service card itself.)
	if strings.Contains(body, "lands in a later release") {
		t.Fatalf("settings page still shows the M4 disabled GDPR stubs:\n%s", body)
	}
	gdpr := body[strings.Index(body, `data-gdpr="self-service"`):]
	if strings.Contains(gdpr, "disabled") {
		t.Fatalf("GDPR self-service controls must not be disabled:\n%s", gdpr)
	}
}
