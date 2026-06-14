package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

type stubHostStore struct{}

func (stubHostStore) GetHost(context.Context, string) (*store.Host, error) {
	return nil, store.ErrNotFound
}

type stubUpserter struct{}

func (stubUpserter) GetHostByGoogleSub(context.Context, string) (*store.Host, error) {
	return nil, store.ErrNotFound
}
func (stubUpserter) CreateHost(context.Context, store.CreateHostParams) (*store.Host, error) {
	return &store.Host{ID: "x"}, nil
}

func testRouter(t *testing.T, rl *RateLimiter) http.Handler {
	t.Helper()
	ring, err := auth.NewKeyRing("router-test-secret-aaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("key ring: %v", err)
	}
	authn := auth.NewAuthenticator(ring, stubHostStore{}, false)
	oauth := auth.NewGoogleOAuth(auth.GoogleConfig{ClientID: "c", ClientSecret: "s", BaseURL: "https://gp.example"}, authn, stubUpserter{})
	h, err := NewRouter(RouterConfig{
		SourceURL:   testSourceURL,
		Hub:         signaling.NewHub(nil, nil),
		OAuth:       oauth,
		Auth:        authn,
		RateLimiter: rl,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return h
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestRouter_LandingHasCSPAndSource(t *testing.T) {
	h := testRouter(t, NewRateLimiter(1000, 1000))
	rec := do(h, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ code = %d", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("/ missing CSP header")
	}
	if !strings.Contains(rec.Body.String(), testSourceURL) {
		t.Error("/ missing source link")
	}
}

func TestRouter_Healthz(t *testing.T) {
	h := testRouter(t, NewRateLimiter(1000, 1000))
	rec := do(h, http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Fatalf("/healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestRouter_GoogleLoginRedirects(t *testing.T) {
	h := testRouter(t, NewRateLimiter(1000, 1000))
	rec := do(h, http.MethodGet, "/auth/google")
	if rec.Code != http.StatusFound {
		t.Fatalf("/auth/google code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "google.com") {
		t.Errorf("redirect = %q, want google", loc)
	}
}

func TestRouter_LogoutClearsSession(t *testing.T) {
	h := testRouter(t, NewRateLimiter(1000, 1000))
	rec := do(h, http.MethodPost, "/auth/logout")
	if rec.Code != http.StatusFound {
		t.Fatalf("/auth/logout code = %d, want 302", rec.Code)
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout should expire the session cookie")
	}
}

func TestRouter_AuthRoutesRateLimited(t *testing.T) {
	h := testRouter(t, NewRateLimiter(0, 1)) // 1 burst, no refill
	first := do(h, http.MethodGet, "/auth/google")
	second := do(h, http.MethodGet, "/auth/google")
	if first.Code != http.StatusFound {
		t.Fatalf("first /auth/google = %d, want 302", first.Code)
	}
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second /auth/google = %d, want 429", second.Code)
	}
}

func TestRouter_DevLoginAbsentByDefault(t *testing.T) {
	h := testRouter(t, NewRateLimiter(1000, 1000))
	if rec := do(h, http.MethodGet, "/auth/dev"); rec.Code != http.StatusNotFound {
		t.Fatalf("/auth/dev should be 404 without a dev-login handler, got %d", rec.Code)
	}
}
