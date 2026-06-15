//go:build dev

package auth

import (
	"errors"
	"net"
	"net/http"

	"github.com/rock3r/guest-pass/internal/store"
)

// devHostGoogleSub is the synthetic google_sub for the local dev host.
const devHostGoogleSub = "dev-local-host"

// DevLogin mints a session for a fixed local dev host WITHOUT Google (AD-8), for local
// development and hermetic tests. It is compiled ONLY under the `dev` build tag, so it
// does not exist in a release binary at all; config additionally refuses AUTH_MODE=dev
// in a release build and requires a loopback BASE_URL in a dev build (RF-4). The dev
// host is active + admin so a developer can exercise every route locally.
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
		host, err := hosts.GetHostByGoogleSub(r.Context(), devHostGoogleSub)
		if errors.Is(err, store.ErrNotFound) {
			host, err = hosts.CreateHost(r.Context(), store.CreateHostParams{
				GoogleSub: devHostGoogleSub,
				Email:     email,
				Name:      name,
				Status:    store.HostActive,
				IsAdmin:   true,
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
