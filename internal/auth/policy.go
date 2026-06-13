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

// loginDecision is the outcome of evaluating a first-time sign-in against the
// onboarding policy. When allowed is false the login is refused and NO host row is
// created (so a later policy change is re-evaluated on the next login).
type loginDecision struct {
	status  string
	isAdmin bool
	allowed bool
}

// decideNewHost decides what to do with a first-time sign-in by email:
//   - the ADMIN_EMAIL match is the owner: active + is_admin (D-14);
//   - open: active immediately (email-verified is enforced upstream, D-36);
//   - allowlist: active if the email is allowlisted, otherwise REFUSED — only
//     allowlisted emails (plus the admin) may become hosts (DEPLOYMENT §4), and not
//     persisting the miss means adding the email later just works on re-login;
//   - approval (and any unknown mode, fail-safe): pending until an admin approves (D-28).
func (p LoginPolicy) decideNewHost(email string) loginDecision {
	if p.AdminEmail != "" && equalEmail(email, p.AdminEmail) {
		return loginDecision{status: store.HostActive, isAdmin: true, allowed: true}
	}
	switch p.SignupMode {
	case config.SignupModeOpen:
		return loginDecision{status: store.HostActive, allowed: true}
	case config.SignupModeAllowlist:
		for _, allowed := range p.AllowedHosts {
			if equalEmail(email, allowed) {
				return loginDecision{status: store.HostActive, allowed: true}
			}
		}
		return loginDecision{allowed: false}
	default: // approval, or any unrecognized mode → fail safe to pending
		return loginDecision{status: store.HostPending, allowed: true}
	}
}

func equalEmail(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
