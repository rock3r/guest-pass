package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/rock3r/guest-pass/internal/store"
)

// SessionCookie is the name of the host-session JWT cookie. It is set httpOnly +
// SameSite + Secure at login (see the web layer); this package only reads it.
const SessionCookie = "gp_session"

// Session-authz outcomes, returned by AuthenticateSessionToken so every entry point
// (the HTTP middleware and the /ws handshake) maps them to the same status. Callers
// branch with errors.Is.
//
//   - ErrUnauthorized — re-auth: missing/invalid/expired token, or the host no longer
//     exists. The credential itself is no good (→ 401).
//   - ErrForbidden — the host exists but is not active (pending/suspended), read LIVE
//     so a suspend takes effect mid-session (→ 403).
var (
	ErrUnauthorized = errors.New("auth: unauthorized")
	ErrForbidden    = errors.New("auth: forbidden")
)

// HostStore is the narrow slice of the store the authz middleware needs. *store.Store
// satisfies it; tests pass a fake. Reading the host on every request is what makes
// authz live (EN-6).
type HostStore interface {
	GetHost(ctx context.Context, id string) (*store.Host, error)
}

type ctxKey int

const hostCtxKey ctxKey = iota

// Authenticator authenticates the session cookie and authorizes against live host state.
type Authenticator struct {
	ring   *KeyRing
	hosts  HostStore
	secure bool // set the session cookie Secure flag (true in production HTTPS)
}

// NewAuthenticator builds an Authenticator from a key ring and a host store. secure
// controls the session cookie's Secure attribute — true under HTTPS, false on a
// loopback dev origin (where Secure cookies aren't sent over plain http).
func NewAuthenticator(ring *KeyRing, hosts HostStore, secure bool) *Authenticator {
	return &Authenticator{ring: ring, hosts: hosts, secure: secure}
}

// HostFromContext returns the authenticated host attached by RequireHost/RequireAdmin.
func HostFromContext(ctx context.Context) (*store.Host, bool) {
	h, ok := ctx.Value(hostCtxKey).(*store.Host)
	return h, ok
}

// RequireHost authenticates the session cookie, loads the host LIVE from the DB, requires
// status=active, and puts the host in the request context (EN-6). It rejects with 401
// when the session is missing/invalid/expired or the host no longer exists, and 403 when
// the host is not active (pending/suspended), so a suspend takes effect mid-session.
func (a *Authenticator) RequireHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, ok := a.authenticate(w, r)
		if !ok {
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), hostCtxKey, host)))
	})
}

// RequireAdmin is RequireHost plus a live is_admin check (D-14).
func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return a.RequireHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _ := HostFromContext(r.Context())
		if host == nil || !host.IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// AuthenticateSessionToken verifies a raw session JWT and loads its host LIVE from the
// DB, requiring status=active (EN-6). It is the single source of truth for host-session
// authz, shared by the HTTP middleware and the /ws handshake. It returns ErrUnauthorized
// (invalid/expired token or unknown host), ErrForbidden (host not active), or a wrapped
// store error on an infrastructure failure. The token is never logged (EN-16).
func (a *Authenticator) AuthenticateSessionToken(ctx context.Context, raw string) (*store.Host, error) {
	hostID, err := a.ring.Verify(raw)
	if err != nil {
		// Expired and otherwise-invalid sessions both route to re-auth.
		return nil, ErrUnauthorized
	}
	host, err := a.hosts.GetHost(ctx, hostID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if host.Status != store.HostActive { // live status read (EN-6): suspend/pending take effect now
		return nil, ErrForbidden
	}
	return host, nil
}

// authenticate validates the session cookie and loads the host live, enforcing
// status=active. On any failure it writes the response and returns ok=false. The token
// (cookie value) is never logged (EN-16).
func (a *Authenticator) authenticate(w http.ResponseWriter, r *http.Request) (*store.Host, bool) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	host, err := a.AuthenticateSessionToken(r.Context(), cookie.Value)
	switch {
	case errors.Is(err, ErrUnauthorized):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	case errors.Is(err, ErrForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	return host, true
}
