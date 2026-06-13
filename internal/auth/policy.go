package auth

import (
	"strings"

	"github.com/rock3r/guest-pass/internal/config"
	"github.com/rock3r/guest-pass/internal/store"
)

// LoginPolicy decides a first-time host's status and admin flag from the instance's
// onboarding configuration (D-28 / D-36 / §9.3).
type LoginPolicy struct {
	SignupMode   string   // config.SignupMode* (open | approval | allowlist)
	AdminEmail   string   // the owner; first sign-in matching this becomes is_admin (D-14)
	AllowedHosts []string // consulted only when SignupMode=allowlist
}

// statusForNewHost returns the status and is_admin for a first-time sign-in by email:
//   - the ADMIN_EMAIL match is the owner: active + is_admin (D-14);
//   - open: active immediately (email-verified is enforced upstream, D-36);
//   - allowlist: active if the email is allowlisted, else pending;
//   - approval (and any unknown mode, fail-safe): pending until an admin approves (D-28).
func (p LoginPolicy) statusForNewHost(email string) (status string, isAdmin bool) {
	if p.AdminEmail != "" && equalEmail(email, p.AdminEmail) {
		return store.HostActive, true
	}
	switch p.SignupMode {
	case config.SignupModeOpen:
		return store.HostActive, false
	case config.SignupModeAllowlist:
		for _, allowed := range p.AllowedHosts {
			if equalEmail(email, allowed) {
				return store.HostActive, false
			}
		}
		return store.HostPending, false
	default: // approval, or any unrecognized mode → fail safe to pending
		return store.HostPending, false
	}
}

func equalEmail(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
