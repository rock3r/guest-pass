//go:build !dev

package config

import (
	"errors"
	"testing"
)

// In a release build the dev-auth seam is not compiled in (devBuild == false), so
// AUTH_MODE=dev must be a hard startup error regardless of BASE_URL (AD-8 / RF-4 /
// TESTING §3). This is the M1 invariant test for "AUTH_MODE=dev refused outside dev".
func TestLoad_DevAuthRefusedInReleaseBuild(t *testing.T) {
	if devBuild {
		t.Skip("dev build; release-mode invariant tested elsewhere")
	}
	cases := []struct {
		name    string
		baseURL string
	}{
		{"loopback base url still refused", "http://localhost:8137"},
		{"non-loopback base url refused", "https://guest-pass.link"},
		{"empty base url refused", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["AUTH_MODE"] = "dev"
			env["BASE_URL"] = tc.baseURL
			_, err := envLoad(env)
			if !errors.Is(err, ErrDevAuthInRelease) {
				t.Fatalf("expected ErrDevAuthInRelease, got %v", err)
			}
		})
	}
}
