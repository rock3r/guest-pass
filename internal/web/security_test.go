package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders_CSPAndHardening(t *testing.T) {
	var ctxNonce string
	h := SecurityHeaders(SecurityOptions{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxNonce = NonceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self' 'nonce-", "style-src 'self'", "connect-src 'self' wss:", "base-uri 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got %q", want, csp)
		}
	}
	if ctxNonce == "" || !strings.Contains(csp, "'nonce-"+ctxNonce+"'") {
		t.Errorf("nonce mismatch: ctx=%q csp=%q", ctxNonce, csp)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestSecurityHeaders_NoTURNHostByDefault(t *testing.T) {
	h := SecurityHeaders(SecurityOptions{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	// STUN-only: connect-src has self + wss: but no extra host token.
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "turn.") {
		t.Errorf("unexpected TURN host in connect-src: %q", csp)
	}
}

func TestSecurityHeaders_TURNHostIncluded(t *testing.T) {
	h := SecurityHeaders(SecurityOptions{TURNHost: "turns://turn.example.org:5349"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "turn.example.org") {
		t.Errorf("CSP connect-src should include the TURN host; got %q", csp)
	}
}

func TestSecurityHeaders_NonceIsPerRequest(t *testing.T) {
	var nonces []string
	h := SecurityHeaders(SecurityOptions{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonces = append(nonces, NonceFromContext(r.Context()))
	}))
	for i := 0; i < 2; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	if len(nonces) != 2 || nonces[0] == nonces[1] || nonces[0] == "" {
		t.Errorf("nonces not unique per request: %v", nonces)
	}
}
