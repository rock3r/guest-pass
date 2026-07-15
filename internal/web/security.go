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
	// TURNHost, when non-empty, is added to connect-src for the instance-level TURN relay.
	// Host-owned TURN relays use the turn:/turns: scheme sources included unconditionally.
	TURNHost string
	// Secure is true for an HTTPS origin. When false (a plain-HTTP dev/loopback origin),
	// connect-src also allows ws: so the signaling socket isn't CSP-blocked — some
	// browsers don't treat 'self' as covering WebSocket schemes. Production stays
	// wss:-only.
	Secure bool
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
			h.Set("Content-Security-Policy", buildCSP(nonce, opts.TURNHost, opts.Secure))
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), nonceKey{}, nonce)))
		})
	}
}

// buildCSP assembles the policy. connect-src includes the signaling endpoint (wss:,
// plus ws: on a non-secure dev origin), any configured instance relay, and the TURN
// schemes so a host's saved BYO relay is usable (CONVENTIONS §3.5).
func buildCSP(nonce, turnHost string, secure bool) string {
	connect := "connect-src 'self' wss: turn: turns:"
	if !secure {
		connect += " ws:" // plain-HTTP dev origin: the signaling socket is ws://
	}
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

// CSPTURNHost extracts the host[:port] from a TURN URL (turn:/turns:[//]host:port[?...])
// for use as a connect-src source (CONVENTIONS §3.5). It returns "" for an empty URL —
// the STUN-only default (D-38), which keeps the policy tight.
func CSPTURNHost(turnURL string) string {
	s := strings.TrimSpace(turnURL)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else if i := strings.Index(s, ":"); i >= 0 {
		s = s[i+1:] // strip the turn:/turns: scheme
	}
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[:i] // drop ?transport=… query
	}
	return strings.TrimSpace(s)
}

func randomNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}
