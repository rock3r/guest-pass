package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

// SecurityOptions configures the security-headers middleware.
type SecurityOptions struct {
	// TURNHost, when non-empty, is added to connect-src so the browser may reach a
	// configured TURN relay. Empty in the STUN-only default (D-38), keeping the policy
	// tight.
	TURNHost string
}

type nonceKey struct{}

// NonceFromContext returns the per-request CSP script nonce, for the island-bootstrap
// inline <script nonce="…"> (CONVENTIONS §3.5). Empty if the middleware didn't run.
func NonceFromContext(ctx context.Context) string {
	n, _ := ctx.Value(nonceKey{}).(string)
	return n
}

// SecurityHeaders sets a strict Content-Security-Policy with a fresh per-request script
// nonce, Referrer-Policy: no-referrer (RF-24, so URL tokens can't leak via Referer), and
// related hardening headers (CONVENTIONS §3.5 / DESIGN §7.5). The nonce is exposed via
// the request context for templates.
func SecurityHeaders(opts SecurityOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nonce, err := randomNonce()
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			h := w.Header()
			h.Set("Content-Security-Policy", buildCSP(nonce, opts.TURNHost))
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce)))
		})
	}
}

// buildCSP assembles the policy. connect-src includes the wss: signaling endpoint and a
// TURN host only when one is configured (CONVENTIONS §3.5).
func buildCSP(nonce, turnHost string) string {
	connect := "connect-src 'self' wss:"
	if turnHost != "" {
		connect += " " + turnHost
	}
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'self' 'nonce-" + nonce + "'",
		"style-src 'self'",
		connect,
		"img-src 'self' data:",
		"font-src 'self'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}

func randomNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}
