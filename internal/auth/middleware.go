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

// authenticate validates the session cookie and loads the host live, enforcing
// status=active. On any failure it writes the response and returns ok=false. The token
// (cookie value) is never logged (EN-16).
func (a *Authenticator) authenticate(w http.ResponseWriter, r *http.Request) (*store.Host, bool) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	hostID, err := a.ring.Verify(cookie.Value)
	if err != nil {
		// Expired and otherwise-invalid sessions both route to re-auth (401).
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	host, err := a.hosts.GetHost(r.Context(), hostID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if host.Status != store.HostActive { // live status read (EN-6): suspend/pending take effect now
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return host, true
}
