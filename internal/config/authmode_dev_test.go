//go:build dev

package config

import (
	"errors"
	"testing"
)

// In a dev build (devBuild == true) the AUTH_MODE=dev seam is compiled in, but it must
// refuse a non-loopback BASE_URL so a dev binary can never be pointed at a real origin
// (AD-8 / RF-4). Run with: go test -tags dev ./internal/config/...
func TestLoad_DevAuthRequiresLoopbackBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		wantErr error // nil => clean load
	}{
		{"localhost ok", "http://localhost:8137", nil},
		{"127.0.0.1 ok", "http://127.0.0.1:8137", nil},
		{"ipv6 loopback ok", "http://[::1]:8137", nil},
		{"non-loopback host refused", "https://guest-pass.link", ErrDevBaseURLNotLoopback},
		{"public ip refused", "http://203.0.113.4:8137", ErrDevBaseURLNotLoopback},
		{"empty base url refused", "", ErrDevBaseURLNotLoopback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["AUTH_MODE"] = "dev"
			env["BASE_URL"] = tc.baseURL
			c, err := envLoad(env)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected clean load, got %v", err)
				}
				if c.AuthMode != AuthModeDev {
					t.Fatalf("AuthMode = %q, want %q", c.AuthMode, AuthModeDev)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// In dev mode the fake host session is minted without Google (AD-8), so Google OAuth
// credentials are NOT required even though they are required in production.
func TestLoad_DevAuthDoesNotRequireGoogleCreds(t *testing.T) {
	env := validEnv()
	env["AUTH_MODE"] = "dev"
	env["BASE_URL"] = "http://localhost:8137"
	delete(env, "GOOGLE_CLIENT_ID")
	delete(env, "GOOGLE_CLIENT_SECRET")
	if _, err := envLoad(env); err != nil {
		t.Fatalf("dev mode should not require Google creds, got %v", err)
	}
}
