package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// M5.5 / AC-2 (DESIGN §6 `notfound`): an unmatched route renders the 404 state screen for a
// navigation, and a terse body for a fetch/XHR — not chi's bare "404 page not found".
func TestErrorScreens_NotFoundRendersScreen(t *testing.T) {
	a := newAPIHarness(t)

	rec := a.navReq(t, "/no-such-page", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown route nav = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("404 nav Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "find that page") {
		t.Fatalf("404 screen missing the not-found copy:\n%s", rec.Body.String())
	}

	// A fetch/XHR (no Accept: text/html) keeps the terse body — no screen.
	rec = a.req(t, http.MethodGet, "/no-such-page", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("fetch 404 = %d, want 404", rec.Code)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "find that page") {
		t.Fatalf("fetch 404 should get the terse body, not the screen:\n%s", rec.Body.String())
	}
}

// M5.5 / AC-2 (DESIGN §6 pass-expired / link-off): the /p/{token} landing renders the designed
// state screen for a revoked, expired, or unknown/rotated link (navigation), distinguishing the
// expired screen (D-5 grace copy) from the opaque link-off screen.
func TestErrorScreens_PassLandingStates(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "passhost")
	streamID := a.createStream(t, cookie, "Show")

	mk := func(status string) string {
		raw, err := token.Mint()
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if _, err := a.store.CreatePass(context.Background(), store.CreatePassParams{
			StreamID: streamID, Role: store.RoleGuest, TokenHash: a.hasher.Hash(raw), Status: status,
		}); err != nil {
			t.Fatalf("CreatePass(%s): %v", status, err)
		}
		return raw
	}

	// Unknown/rotated tokens keep the 404 contract (indistinguishable from never-existed) but still
	// render the friendly link-off screen; revoked/expired passes are 410 Gone with distinct copy.
	cases := []struct {
		name, token, want string
		status            int
	}{
		{"revoked → link-off", mk(store.PassRevoked), "turned off", http.StatusGone},
		{"expired → expired screen", mk(store.PassExpired), "expired", http.StatusGone},
		{"unknown/rotated → link-off", "totally-unknown-token", "turned off", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := a.navReq(t, "/p/"+tc.token, nil)
			if rec.Code != tc.status {
				t.Fatalf("%s = %d, want %d", tc.name, rec.Code, tc.status)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Fatalf("%s Content-Type = %q, want text/html", tc.name, ct)
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), tc.want) {
				t.Fatalf("%s screen missing %q:\n%s", tc.name, tc.want, rec.Body.String())
			}
		})
	}
}

// navReq issues a GET that looks like a top-level browser navigation (Accept: text/html), so
// the auth middleware renders the server HTML error screen rather than the plain-text denial
// the JSON/fetch API callers get. This is the only thing that distinguishes an /app navigation
// from a greenroom-island fetch() to /api/* (which sends Accept: */*).
func (a *apiHarness) navReq(t *testing.T, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// M5.5 / EN-6: a suspended host who navigates to a host-app route gets a rendered, explanatory
// "account suspended" screen (what it means + how to appeal) instead of a bare "forbidden" body.
// The status stays 403 (the authz decision is unchanged — only its presentation).
func TestErrorScreens_SuspendedHostGetsScreen(t *testing.T) {
	a := newAPIHarness(t)
	_, suspended := a.hostWithStatus(t, "suspended-host", store.HostSuspended)

	for _, route := range []string{"/app", "/greenroom"} {
		rec := a.navReq(t, route, suspended)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("suspended host GET %s = %d, want 403", route, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("suspended host GET %s Content-Type = %q, want text/html", route, ct)
		}
		body := strings.ToLower(rec.Body.String())
		if strings.TrimSpace(body) == "forbidden" {
			t.Fatalf("suspended host GET %s still returns the bare forbidden body", route)
		}
		if !strings.Contains(body, "suspended") {
			t.Fatalf("suspended host GET %s body missing the suspension explanation:\n%s", route, rec.Body.String())
		}
		// Some appeal/contact guidance must be present (D-27 gave no graceful UX before).
		if !strings.Contains(body, "appeal") && !strings.Contains(body, "administrator") {
			t.Fatalf("suspended host GET %s body missing appeal/contact guidance:\n%s", route, rec.Body.String())
		}
	}
}

// M5.5 / D-28: a pending host (signed in, awaiting approval) gets an "awaiting approval" screen,
// distinct from the suspended one, rather than a bare forbidden body. Still 403.
func TestErrorScreens_PendingHostGetsScreen(t *testing.T) {
	a := newAPIHarness(t)
	_, pending := a.hostWithStatus(t, "pending-host", store.HostPending)

	rec := a.navReq(t, "/app", pending)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pending host GET /app = %d, want 403", rec.Code)
	}
	body := strings.ToLower(rec.Body.String())
	if !strings.Contains(body, "approval") && !strings.Contains(body, "pending") {
		t.Fatalf("pending host GET /app body missing the awaiting-approval explanation:\n%s", rec.Body.String())
	}
	if strings.Contains(body, "suspended") {
		t.Fatalf("pending host GET /app wrongly shows the suspended screen:\n%s", rec.Body.String())
	}
}

// M5.5 / D-14: a regular (non-admin) active host who navigates to /admin gets a friendly
// "you don't have access" screen, not a bare forbidden body. Still 403.
func TestErrorScreens_NonAdminGetsAdminScreen(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice") // active, not an admin

	rec := a.navReq(t, "/admin", alice)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin GET /admin = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("non-admin GET /admin Content-Type = %q, want text/html", ct)
	}
	body := strings.ToLower(rec.Body.String())
	if strings.TrimSpace(body) == "forbidden" {
		t.Fatalf("non-admin GET /admin still returns the bare forbidden body")
	}
	if !strings.Contains(body, "access") {
		t.Fatalf("non-admin GET /admin body missing the no-access explanation:\n%s", rec.Body.String())
	}
}

// M5.5: a navigation with no/invalid session (401, re-auth) gets a "sign in" screen — keeping the
// 401 (re-auth) vs 403 (suspended/forbidden) distinction crisp.
func TestErrorScreens_UnauthenticatedGetsSignInScreen(t *testing.T) {
	a := newAPIHarness(t)

	rec := a.navReq(t, "/app", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon GET /app = %d, want 401", rec.Code)
	}
	body := strings.ToLower(rec.Body.String())
	if !strings.Contains(body, "sign in") && !strings.Contains(body, "sign-in") {
		t.Fatalf("anon GET /app body missing the sign-in prompt:\n%s", rec.Body.String())
	}
}

// M5.5: the API/fetch callers (greenroom island → /api/*) are NOT changed — they still get the
// terse plain-text denial, never a full HTML page (which a JSON client would choke on). This is
// what the Accept-based content negotiation protects.
func TestErrorScreens_APIClientStillGetsPlainText(t *testing.T) {
	a := newAPIHarness(t)
	_, suspended := a.hostWithStatus(t, "suspended-host", store.HostSuspended)

	// a.req sets no "Accept: text/html" — it mimics a fetch()/XHR, not a navigation.
	rec := a.req(t, http.MethodGet, "/api/streams", "", suspended)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("suspended host GET /api/streams = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Fatalf("API denial Content-Type = %q, should stay plain text for fetch clients", ct)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "forbidden" {
		t.Fatalf("API denial body = %q, want the terse \"forbidden\"", got)
	}
}
