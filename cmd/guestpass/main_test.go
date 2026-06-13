package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/config"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/web"
)

// serve must fail closed (EN-14) before binding a socket: if config.Load rejects the
// environment — here, an empty JWT_SECRET — serve returns that error and never starts
// the server. Without the config wiring this test hangs in ListenAndServe (caught by
// the test timeout), which is the red state.
func TestServeFailsClosedWithoutJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("AUTH_MODE", "")
	t.Setenv("TURN_URL", "")

	err := serve(":0")
	if !errors.Is(err, config.ErrSecretFailClosed) {
		t.Fatalf("expected serve to fail closed with ErrSecretFailClosed, got %v", err)
	}
}

// TestBuildHandler_Wiring proves the full config→store→auth→router assembly serves the
// health endpoint and the landing page (with CSP + the §13 source link), without binding
// a socket.
func TestBuildHandler_Wiring(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		BaseURL:            "http://localhost:8137",
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		JWTSecret:          "build-handler-test-secret-aaaaaaaaaaaaaa",
		TokenSecret:        "build-handler-test-token-secret-bbbbbbbb",
		SignupMode:         config.SignupModeOpen,
		AdminEmail:         "admin@example.com",
		TURNURL:            "turns:turn.example.org:5349",
		MailMode:           config.MailModeLog,
	}
	h, err := buildHandler(cfg, st, signaling.NewHub(), web.NewRateLimiter(1000, 1000), web.NewRateLimiter(1000, 1000), nil)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	hz := httptest.NewRecorder()
	h.ServeHTTP(hz, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if hz.Code != http.StatusOK || strings.TrimSpace(hz.Body.String()) != "ok" {
		t.Fatalf("/healthz = %d %q", hz.Code, hz.Body.String())
	}

	root := httptest.NewRecorder()
	h.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK {
		t.Fatalf("/ = %d", root.Code)
	}
	csp := root.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("/ missing CSP header")
	}
	if !strings.Contains(csp, "turn.example.org:5349") {
		t.Errorf("CSP connect-src should include the configured TURN host; got %q", csp)
	}
	if !strings.Contains(root.Body.String(), "AGPL-3.0") {
		t.Error("/ missing AGPL §13 source link")
	}
}
