package config

import (
	"errors"
	"reflect"
	"testing"
)

// validEnv returns a minimal environment that loads cleanly in any build: a real
// JWT_SECRET, STUN-only (no TURN), production AUTH_MODE. Tests override single keys.
func validEnv() map[string]string {
	return map[string]string{
		"JWT_SECRET": "Zr8kQv2xN7pL4wT9aB6cD3eF1gH5jK0mPqRsTuVwXyZ", // not a placeholder
	}
}

// envLoad loads a Config from a map-backed getenv seam (no process-env mutation).
func envLoad(env map[string]string) (*Config, error) {
	return load(func(k string) string { return env[k] })
}

func TestLoad_JWTSecretFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		wantOK bool
	}{
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"placeholder changeme", "changeme", false},
		{"placeholder mixed case + spaces", "  ChangeMe  ", false},
		{"placeholder secret", "secret", false},
		{"real secret", "Zr8kQv2xN7pL4wT9aB6cD3eF1gH5jK0mPqRsTuVwXyZ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["JWT_SECRET"] = tc.secret
			_, err := envLoad(env)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("expected clean load, got %v", err)
				}
				return
			}
			if !errors.Is(err, ErrSecretFailClosed) {
				t.Fatalf("expected ErrSecretFailClosed, got %v", err)
			}
		})
	}
}

func TestLoad_TURNSecretFailsClosedOnlyWhenTURNEnabled(t *testing.T) {
	cases := []struct {
		name       string
		turnURL    string
		turnSecret string
		wantErr    error // nil => clean load
	}{
		{"stun-only, no turn secret", "", "", nil},
		{"stun-only ignores placeholder turn secret", "", "changeme", nil},
		{"turn enabled, empty secret fails", "turns:turn.example.org:5349", "", ErrSecretFailClosed},
		{"turn enabled, placeholder secret fails", "turns:turn.example.org:5349", "changeme", ErrSecretFailClosed},
		{"turn enabled, real secret ok", "turns:turn.example.org:5349", "h9P2qWcN6vR4tY8uA1sD5fG3jK7lZxBmQ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["TURN_URL"] = tc.turnURL
			env["TURN_SECRET"] = tc.turnSecret
			_, err := envLoad(env)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected clean load, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoad_UnknownAuthModeRejected(t *testing.T) {
	env := validEnv()
	env["AUTH_MODE"] = "staging"
	_, err := envLoad(env)
	if !errors.Is(err, ErrUnknownAuthMode) {
		t.Fatalf("expected ErrUnknownAuthMode, got %v", err)
	}
}

func TestLoad_ParsesFields(t *testing.T) {
	env := validEnv()
	env["BASE_URL"] = "https://guest-pass.link"
	env["GOOGLE_CLIENT_ID"] = "client-id"
	env["SIGNUP_MODE"] = "open"
	env["MAIL_MODE"] = "log"
	env["ALLOWED_HOSTS"] = "a@example.com, b@example.com ,, c@example.com"
	env["CODEC_OPTIN"] = "vp9, av1"
	env["TURN_URL"] = "turns:turn.example.org:5349"
	env["TURN_SECRET"] = "h9P2qWcN6vR4tY8uA1sD5fG3jK7lZxBmQ"

	c, err := envLoad(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.BaseURL != "https://guest-pass.link" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c.MailMode != MailModeLog {
		t.Errorf("MailMode = %q", c.MailMode)
	}
	if !c.TURNEnabled() {
		t.Errorf("TURNEnabled() = false, want true")
	}
	if got, want := c.AllowedHosts, []string{"a@example.com", "b@example.com", "c@example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AllowedHosts = %v, want %v", got, want)
	}
	if got, want := c.CodecOptin, []string{"vp9", "av1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CodecOptin = %v, want %v", got, want)
	}
}
