package auth

import (
	"testing"

	"github.com/rock3r/guest-pass/internal/config"
	"github.com/rock3r/guest-pass/internal/store"
)

func TestDecideNewHost(t *testing.T) {
	const admin = "owner@example.com"
	cases := []struct {
		name   string
		policy LoginPolicy
		email  string
		want   loginDecision
	}{
		{"admin email is owner", LoginPolicy{SignupMode: config.SignupModeApproval, AdminEmail: admin}, "OWNER@example.com", loginDecision{store.HostActive, true, true}},
		{"open is active", LoginPolicy{SignupMode: config.SignupModeOpen, AdminEmail: admin}, "a@example.com", loginDecision{store.HostActive, false, true}},
		{"approval is pending", LoginPolicy{SignupMode: config.SignupModeApproval, AdminEmail: admin}, "a@example.com", loginDecision{store.HostPending, false, true}},
		{"allowlist allowed is active", LoginPolicy{SignupMode: config.SignupModeAllowlist, AdminEmail: admin, AllowedHosts: []string{"a@example.com"}}, "A@example.com", loginDecision{store.HostActive, false, true}},
		// A non-allowlisted email is NOT allowed to become a host (DEPLOYMENT §4) — no
		// persistent pending row, so re-login re-evaluates the current allowlist.
		{"allowlist not allowed is rejected", LoginPolicy{SignupMode: config.SignupModeAllowlist, AdminEmail: admin, AllowedHosts: []string{"a@example.com"}}, "b@example.com", loginDecision{allowed: false}},
		{"unknown mode fails safe to pending", LoginPolicy{SignupMode: "weird", AdminEmail: admin}, "a@example.com", loginDecision{store.HostPending, false, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.decideNewHost(tc.email); got != tc.want {
				t.Errorf("decideNewHost(%q) = %+v, want %+v", tc.email, got, tc.want)
			}
		})
	}
}
