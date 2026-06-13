//go:build !dev

package main

import (
	"net/http"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/config"
)

// devLoginHandler is nil in a release build: the AUTH_MODE=dev sign-in seam is not
// compiled in at all (AD-8/RF-4), so /auth/dev is never registered.
func devLoginHandler(*config.Config, *auth.Authenticator, auth.HostUpserter) http.HandlerFunc {
	return nil
}
