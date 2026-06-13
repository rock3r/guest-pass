//go:build dev

package auth

import (
	"errors"
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
func (a *Authenticator) DevLogin(hosts HostUpserter, email, name string) http.HandlerFunc {
	if email == "" {
		email = "dev@localhost"
	}
	if name == "" {
		name = "Dev Host"
	}
	return func(w http.ResponseWriter, r *http.Request) {
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
		http.Redirect(w, r, "/", http.StatusFound)
	}
}
