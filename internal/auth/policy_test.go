package auth

import (
	"testing"

	"github.com/rock3r/guest-pass/internal/config"
	"github.com/rock3r/guest-pass/internal/store"
)

func TestStatusForNewHost(t *testing.T) {
	const admin = "owner@example.com"
	cases := []struct {
		name       string
		policy     LoginPolicy
		email      string
		wantStatus string
		wantAdmin  bool
	}{
		{"admin email is owner", LoginPolicy{SignupMode: config.SignupModeApproval, AdminEmail: admin}, "OWNER@example.com", store.HostActive, true},
		{"open is active", LoginPolicy{SignupMode: config.SignupModeOpen, AdminEmail: admin}, "a@example.com", store.HostActive, false},
		{"approval is pending", LoginPolicy{SignupMode: config.SignupModeApproval, AdminEmail: admin}, "a@example.com", store.HostPending, false},
		{"allowlist allowed is active", LoginPolicy{SignupMode: config.SignupModeAllowlist, AdminEmail: admin, AllowedHosts: []string{"a@example.com"}}, "A@example.com", store.HostActive, false},
		{"allowlist not allowed is pending", LoginPolicy{SignupMode: config.SignupModeAllowlist, AdminEmail: admin, AllowedHosts: []string{"a@example.com"}}, "b@example.com", store.HostPending, false},
		{"unknown mode fails safe to pending", LoginPolicy{SignupMode: "weird", AdminEmail: admin}, "a@example.com", store.HostPending, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotAdmin := tc.policy.statusForNewHost(tc.email)
			if gotStatus != tc.wantStatus || gotAdmin != tc.wantAdmin {
				t.Errorf("statusForNewHost(%q) = (%q, %v), want (%q, %v)", tc.email, gotStatus, gotAdmin, tc.wantStatus, tc.wantAdmin)
			}
		})
	}
}
