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

// DenialReason classifies an authz denial so the presentation layer can render the matching
// screen while the authz DECISION stays here (EN-6). The HTTP status is fixed per reason (see
// Status) and is never the renderer's choice — only the body/format is.
type DenialReason int

const (
	DenyNone         DenialReason = iota // authorized — the zero value; not a denial
	DenyUnauthorized                     // 401 — re-auth: missing/invalid/expired token, or unknown host
	DenyInactive                         // 403 — the host exists but is not active (suspended/pending), read LIVE (EN-6)
	DenyNotAdmin                         // 403 — the host is active but lacks the required authority (not an admin)
	DenyError                            // 500 — infrastructure failure loading the host
)

// Status is the fixed HTTP status for a denial reason, shared by the plain-text fallback and any
// installed DeniedHandler so the two can never disagree on the code (only on the body format).
func (d DenialReason) Status() int {
	switch d {
	case DenyUnauthorized:
		return http.StatusUnauthorized
	case DenyError:
		return http.StatusInternalServerError
	default: // DenyInactive, DenyNotAdmin (and the unreachable DenyNone)
		return http.StatusForbidden
	}
}

// DeniedHandler renders the response for a denied request, owning the whole response (headers,
// status, body). The web layer installs one that content-negotiates a rendered HTML error screen
// for navigations; when none is installed, deny falls back to plain text — so internal/auth keeps
// no dependency on internal/web. host is the LIVE host when one was loaded: non-nil for
// DenyInactive (so the screen can tell suspended from pending) and DenyNotAdmin; nil otherwise.
type DeniedHandler func(w http.ResponseWriter, r *http.Request, reason DenialReason, host *store.Host)

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
	secure bool          // set the session cookie Secure flag (true in production HTTPS)
	denied DeniedHandler // optional renderer for denials; nil falls back to plain http.Error
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

// ContextWithHost attaches an authenticated host to ctx under the same key RequireHost uses, so
// HostFromContext recovers it. It is the symmetric setter for HostFromContext; RequireHost/
// RequireAdmin call the equivalent internally, and tests use it to drive a handler that reads the
// acting host from context without standing up the full middleware chain.
func ContextWithHost(ctx context.Context, h *store.Host) context.Context {
	return context.WithValue(ctx, hostCtxKey, h)
}

// SetDeniedHandler installs the renderer for denied requests (the web layer's HTML error
// screens). With none installed, denials fall back to the plain-text bodies below. It is wired
// once at startup, before the server serves traffic — not concurrency-safe with live requests.
func (a *Authenticator) SetDeniedHandler(h DeniedHandler) { a.denied = h }

// deny writes a denial response: the installed DeniedHandler when present (content-negotiated
// HTML screens), else a terse plain-text body. Either way the status is reason.Status(), so the
// two paths can never disagree on the code. host is forwarded so an HTML screen can specialize
// (suspended vs pending).
func (a *Authenticator) deny(w http.ResponseWriter, r *http.Request, reason DenialReason, host *store.Host) {
	if a.denied != nil {
		a.denied(w, r, reason, host)
		return
	}
	switch reason {
	case DenyUnauthorized:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case DenyError:
		http.Error(w, "internal error", http.StatusInternalServerError)
	default: // DenyInactive, DenyNotAdmin
		http.Error(w, "forbidden", http.StatusForbidden)
	}
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
		next.ServeHTTP(w, r.WithContext(ContextWithHost(r.Context(), host)))
	})
}

// RequireAdmin is RequireHost plus a live is_admin check (D-14).
func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return a.RequireHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _ := HostFromContext(r.Context())
		if host == nil || !host.IsAdmin {
			a.deny(w, r, DenyNotAdmin, host)
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
	host, reason, err := a.resolveSession(ctx, raw)
	switch reason {
	case DenyUnauthorized:
		return nil, ErrUnauthorized
	case DenyInactive:
		return nil, ErrForbidden
	case DenyError:
		return nil, err // wrapped infra error, preserved for callers that map it to 500
	default:
		return host, nil
	}
}

// resolveSession is the shared authz core (EN-6): verify the session token and load the host
// LIVE, classifying the outcome as a DenialReason. It returns the host whenever one was loaded —
// INCLUDING the not-active case (DenyInactive), so the HTTP middleware can render a status-specific
// screen (suspended vs pending) without a second read. err is non-nil only for DenyError (the
// wrapped infra failure). AuthenticateSessionToken adapts this to the sentinel-error contract the
// /ws handshake branches on; the HTTP middleware uses the (host, reason) form directly.
func (a *Authenticator) resolveSession(ctx context.Context, raw string) (*store.Host, DenialReason, error) {
	hostID, err := a.ring.Verify(raw)
	if err != nil {
		// Expired and otherwise-invalid sessions both route to re-auth.
		return nil, DenyUnauthorized, nil
	}
	host, err := a.hosts.GetHost(ctx, hostID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, DenyUnauthorized, nil
	}
	if err != nil {
		return nil, DenyError, err
	}
	if host.Status != store.HostActive { // live status read (EN-6): suspend/pending take effect now
		return host, DenyInactive, nil
	}
	return host, DenyNone, nil
}

// authenticate validates the session cookie and loads the host live, enforcing
// status=active. On any failure it writes the denial (via deny) and returns ok=false. The token
// (cookie value) is never logged (EN-16).
func (a *Authenticator) authenticate(w http.ResponseWriter, r *http.Request) (*store.Host, bool) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		a.deny(w, r, DenyUnauthorized, nil)
		return nil, false
	}
	host, reason, _ := a.resolveSession(r.Context(), cookie.Value)
	if reason != DenyNone {
		a.deny(w, r, reason, host)
		return nil, false
	}
	return host, true
}
