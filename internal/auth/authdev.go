//go:build dev

package auth

import (
	"errors"
	"net"
	"net/http"

	"github.com/rock3r/guest-pass/internal/store"
)

// devHostGoogleSub is the synthetic google_sub for the local dev host (active + admin).
const devHostGoogleSub = "dev-local-host"

// devHost2GoogleSub is a SECOND, non-admin local dev host, selected with /auth/dev?as=host2.
// It exists only so cross-host admin flows can be smoke-tested on a single local instance —
// the D-27 admin suspend-cascade, the §7.7 metadata-only boundary, and the non-admin /admin
// 403 — none of which the single always-admin primary identity can reach. Like the primary
// dev login it is loopback-only and compiled only under the `dev` tag (never in a release).
const devHost2GoogleSub = "dev-local-host-2"

// DevLogin mints a session for a fixed local dev host WITHOUT Google (AD-8), for local
// development and hermetic tests. It is compiled ONLY under the `dev` build tag, so it
// does not exist in a release binary at all; config additionally refuses AUTH_MODE=dev
// in a release build and requires a loopback BASE_URL in a dev build (RF-4). The dev
// host is active + admin so a developer can exercise every route locally; passing
// ?as=host2 signs in as a distinct NON-admin secondary host instead (devHost2GoogleSub).
//
// As defense-in-depth, the handler ALSO rejects non-loopback clients: the dev server
// still listens on all interfaces, so a LAN- or Docker-exposed dev binary must not hand
// an admin session to a remote client — only requests from localhost are honored.
func (a *Authenticator) DevLogin(hosts HostUpserter, email, name string) http.HandlerFunc {
	if email == "" {
		email = "dev@localhost"
	}
	if name == "" {
		name = "Dev Host"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !remoteIsLoopback(r) {
			http.Error(w, "dev login is loopback-only", http.StatusForbidden)
			return
		}
		// Default identity = the active+admin primary dev host. ?as=host2 selects the
		// distinct non-admin secondary (for local cross-host admin smokes); any other value
		// falls back to the primary, so a typo can never silently grant an extra admin.
		sub, hEmail, hName, isAdmin := devHostGoogleSub, email, name, true
		if r.URL.Query().Get("as") == "host2" {
			sub, hEmail, hName, isAdmin = devHost2GoogleSub, "host2@localhost", "Dev Host 2", false
		}
		host, err := hosts.GetHostByGoogleSub(r.Context(), sub)
		if errors.Is(err, store.ErrNotFound) {
			host, err = hosts.CreateHost(r.Context(), store.CreateHostParams{
				GoogleSub: sub,
				Email:     hEmail,
				Name:      hName,
				Status:    store.HostActive,
				IsAdmin:   isAdmin,
			})
		}
		if err != nil {
			http.Error(w, "dev login failed", http.StatusInternalServerError)
			return
		}
		if err := a.SetSession(w, host.ID); err != nil {
			http.Error(w, "session failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/app", http.StatusFound) // land on the host-app dashboard (M4)
	}
}

// remoteIsLoopback reports whether the request's client address is a loopback IP.
func remoteIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
