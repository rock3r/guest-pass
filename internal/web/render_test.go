package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSourceURL = "https://github.com/rock3r/guest-pass/tree/deadbeef"

func TestRenderer_LandingHasSourceLinkAndLicense(t *testing.T) {
	rd, err := newRenderer(testSourceURL, nil, false)
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
		t.Error("landing missing source link to the running build")
	}
	if !strings.Contains(body, "uelicense.eu") {
		t.Error("landing missing UEL v1.0 license mention")
	}
	// The stylesheet loads even without a build manifest (no integrity) so the page is
	// never unstyled; integrity is added only when the manifest is present (next test).
	if !strings.Contains(body, `href="/_gp/app.css"`) {
		t.Error("landing should always link the stylesheet")
	}
	if strings.Contains(body, "integrity=") {
		t.Error("no integrity attribute expected without a manifest")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestRenderer_SigninDevToggle(t *testing.T) {
	// Without dev login, only the Google affordance shows.
	rd, _ := newRenderer(testSourceURL, nil, false)
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
	rdDev, _ := newRenderer(testSourceURL, nil, true)
	rec2 := httptest.NewRecorder()
	rdDev.signin(rec2, httptest.NewRequest(http.MethodGet, "/signin", nil))
	if !strings.Contains(rec2.Body.String(), "/auth/dev") {
		t.Error("signin should show dev login when enabled")
	}
}

func TestRenderer_StyleIntegrityInjectedWhenPresent(t *testing.T) {
	rd, _ := newRenderer(testSourceURL, map[string]string{"app.css": "sha384-abc123"}, false)
	rec := httptest.NewRecorder()
	rd.landing(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `integrity="sha384-abc123"`) {
		t.Error("expected SRI integrity attribute on the stylesheet link")
	}
}

func TestRenderer_VersionsAssetURLWithIntegrity(t *testing.T) {
	rd, _ := newRenderer(testSourceURL, map[string]string{"app.css": "sha384-a+b/c="}, false)
	rec := httptest.NewRecorder()
	rd.landing(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(rec.Body.String(), `href="/_gp/app.css?v=sha384-a%2Bb%2Fc%3D"`) {
		t.Fatalf("stylesheet URL should include the escaped integrity value, got %q", rec.Body.String())
	}
}

// OBS Browser Sources are commonly exempted from an operator's edge-access policy by the
// /s/* path. Their runtime assets must therefore stay beneath that same path: pointing the
// source HTML at the general /_gp asset route lets an Access login redirect replace obs.js or
// obs.css, leaving OBS with a black frame. The source token remains only in its URL query and
// is never rendered into the document.
func TestRenderer_SourcePageScopesAssetsUnderSourcePath(t *testing.T) {
	rd, err := newRenderer(testSourceURL, map[string]string{
		"obs.css": "sha384-a+b/c=",
		"obs.js":  "sha384-d+e/f=",
	}, false)
	if err != nil {
		t.Fatalf("newRenderer: %v", err)
	}

	rec := httptest.NewRecorder()
	rd.sourcePage(rec, httptest.NewRequest(http.MethodGet, "/s/cam-1?token=source-token", nil), "cam-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("source page = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`href="/s/cam-1/_gp/obs.css?v=sha384-a%2Bb%2Fc%3D"`,
		`src="/s/cam-1/_gp/obs.js?v=sha384-d%2Be%2Ff%3D"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("source page missing scoped asset %q in %q", want, body)
		}
	}
	if strings.Contains(body, "source-token") {
		t.Fatal("source token must not be rendered into the source document")
	}
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}
