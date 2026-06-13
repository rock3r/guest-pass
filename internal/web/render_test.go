package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSourceURL = "https://github.com/rock3r/guest-pass/tree/deadbeef"

func TestRenderer_LandingHasSourceLinkAndLicense(t *testing.T) {
	rd, err := newRenderer(testSourceURL, "", false)
	if err != nil {
		t.Fatalf("newRenderer: %v", err)
	}
	rec := httptest.NewRecorder()
	rd.landing(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, testSourceURL) {
		t.Error("landing missing AGPL §13 source link to the running build")
	}
	if !strings.Contains(body, "AGPL-3.0") {
		t.Error("landing missing AGPL-3.0 license mention")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestRenderer_SigninDevToggle(t *testing.T) {
	// Without dev login, only the Google affordance shows.
	rd, _ := newRenderer(testSourceURL, "", false)
	rec := httptest.NewRecorder()
	rd.signin(rec, httptest.NewRequest(http.MethodGet, "/signin", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "/auth/google") {
		t.Error("signin missing Google sign-in link")
	}
	if strings.Contains(body, "/auth/dev") {
		t.Error("signin should not show dev login when disabled")
	}

	// With dev login, the dev affordance appears.
	rdDev, _ := newRenderer(testSourceURL, "", true)
	rec2 := httptest.NewRecorder()
	rdDev.signin(rec2, httptest.NewRequest(http.MethodGet, "/signin", nil))
	if !strings.Contains(rec2.Body.String(), "/auth/dev") {
		t.Error("signin should show dev login when enabled")
	}
}

func TestRenderer_StyleIntegrityInjectedWhenPresent(t *testing.T) {
	rd, _ := newRenderer(testSourceURL, "sha384-abc123", false)
	rec := httptest.NewRecorder()
	rd.landing(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `integrity="sha384-abc123"`) {
		t.Error("expected SRI integrity attribute on the stylesheet link")
	}
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}
