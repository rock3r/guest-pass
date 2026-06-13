//go:build dev

package main

import (
	"net/http"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/config"
)

// devLoginHandler returns the dev sign-in handler when AUTH_MODE=dev (dev build only).
// config already guarantees AUTH_MODE=dev only loads with a loopback BASE_URL (RF-4).
func devLoginHandler(cfg *config.Config, authn *auth.Authenticator, hosts auth.HostUpserter) http.HandlerFunc {
	if cfg.AuthMode != config.AuthModeDev {
		return nil
	}
	return authn.DevLogin(hosts, "", "")
}
