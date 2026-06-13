package config

import (
	"errors"
	"reflect"
	"testing"
)

// validEnv returns a complete production environment that loads cleanly: all required
// vars present (BASE_URL, Google creds, ADMIN_EMAIL, SIGNUP_MODE, RESEND_API_KEY), a
// real JWT_SECRET, STUN-only (no TURN), production AUTH_MODE. Tests override single keys
// to exercise each fail-closed branch.
func validEnv() map[string]string {
	return map[string]string{
		"BASE_URL":             "https://guest-pass.link",
		"GOOGLE_CLIENT_ID":     "client-id.apps.googleusercontent.com",
		"GOOGLE_CLIENT_SECRET": "google-client-secret-placeholder-value-XYZ",
		"JWT_SECRET":           "Zr8kQv2xN7pL4wT9aB6cD3eF1gH5jK0mPqRsTuVwXyZ", // not a placeholder
		"TOKEN_SECRET":         "Tk9sErV2nQ7wL4yA8dF3gH6jK0mPqRsZxBmNcVbQp1",  // not a placeholder
		"ADMIN_EMAIL":          "admin@example.com",
		"SIGNUP_MODE":          "open",
		"RESEND_API_KEY":       "re_resend_api_key_placeholder_value",
		"MAIL_FROM":            "GuestPass <invites@guest-pass.link>",
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
		{"short non-placeholder rejected", "foo", false},
		{"31 chars rejected", "abcdefghijklmnopqrstuvwxyzABCDE", false},
		{"32 chars accepted", "abcdefghijklmnopqrstuvwxyzABCDEF", true},
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
		{"turn enabled, short secret fails", "turns:turn.example.org:5349", "foo", ErrSecretFailClosed},
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

// JWT_SECRET_PREVIOUS is the optional verify-only second key in the kid two-key ring
// (EN-6): unset in steady state, set to the old secret during rotation. When set it
// must be a real secret (fail-closed); when unset the load is clean.
func TestLoad_JWTSecretPrevious(t *testing.T) {
	cases := []struct {
		name   string
		prev   string
		wantOK bool
	}{
		{"unset", "", true},
		{"strong previous ok", "PrevKqW7nR2vT5yA8sD3fG6hJ0lZxBmNcVbQ", true},
		{"short previous rejected", "short", false},
		{"placeholder previous rejected", "changeme", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			if tc.prev != "" {
				env["JWT_SECRET_PREVIOUS"] = tc.prev
			}
			c, err := envLoad(env)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("expected clean load, got %v", err)
				}
				if c.JWTSecretPrevious != tc.prev {
					t.Errorf("JWTSecretPrevious = %q, want %q", c.JWTSecretPrevious, tc.prev)
				}
				return
			}
			if !errors.Is(err, ErrSecretFailClosed) {
				t.Fatalf("expected ErrSecretFailClosed, got %v", err)
			}
		})
	}
}

func TestLoad_RequiredVarsFailClosed(t *testing.T) {
	// Each var, cleared, must make a production load fail closed (CONVENTIONS §1.5 /
	// DEPLOYMENT §3). RESEND_API_KEY is required only when MAIL_MODE != log.
	for _, key := range []string{"BASE_URL", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "ADMIN_EMAIL", "SIGNUP_MODE", "RESEND_API_KEY"} {
		t.Run("missing "+key, func(t *testing.T) {
			env := validEnv()
			delete(env, key)
			_, err := envLoad(env)
			if key == "SIGNUP_MODE" {
				// Empty SIGNUP_MODE is missing-required; a present-but-bad value is
				// ErrInvalidValue (covered separately). Empty -> ErrMissingRequired.
				if !errors.Is(err, ErrMissingRequired) {
					t.Fatalf("expected ErrMissingRequired for empty %s, got %v", key, err)
				}
				return
			}
			if !errors.Is(err, ErrMissingRequired) {
				t.Fatalf("expected ErrMissingRequired for missing %s, got %v", key, err)
			}
		})
	}
}

func TestLoad_ResendKeyNotRequiredInLogMode(t *testing.T) {
	env := validEnv()
	delete(env, "RESEND_API_KEY")
	env["MAIL_MODE"] = "log"
	if _, err := envLoad(env); err != nil {
		t.Fatalf("MAIL_MODE=log should not require RESEND_API_KEY, got %v", err)
	}
}

func TestLoad_InvalidEnumsRejected(t *testing.T) {
	t.Run("bad SIGNUP_MODE", func(t *testing.T) {
		env := validEnv()
		env["SIGNUP_MODE"] = "opn"
		if _, err := envLoad(env); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("expected ErrInvalidValue, got %v", err)
		}
	})
	t.Run("bad MAIL_MODE", func(t *testing.T) {
		env := validEnv()
		env["MAIL_MODE"] = "smtp"
		if _, err := envLoad(env); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("expected ErrInvalidValue, got %v", err)
		}
	})
	for _, mode := range []string{"open", "approval", "allowlist"} {
		t.Run("good SIGNUP_MODE "+mode, func(t *testing.T) {
			env := validEnv()
			env["SIGNUP_MODE"] = mode
			if _, err := envLoad(env); err != nil {
				t.Fatalf("SIGNUP_MODE=%s should load, got %v", mode, err)
			}
		})
	}
}

func TestLoad_TokenSecretFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret string
		wantOK bool
	}{
		{"missing", "", false},
		{"placeholder", "changeme", false},
		{"short", "tooshort", false},
		{"real", "Tk9sErV2nQ7wL4yA8dF3gH6jK0mPqRsZxBmNcVbQp1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			if tc.secret == "" {
				delete(env, "TOKEN_SECRET")
			} else {
				env["TOKEN_SECRET"] = tc.secret
			}
			_, err := envLoad(env)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("expected clean load, got %v", err)
				}
			} else if !errors.Is(err, ErrSecretFailClosed) {
				t.Fatalf("expected ErrSecretFailClosed, got %v", err)
			}
		})
	}
}

func TestLoad_MailFromRequiredUnlessLogMode(t *testing.T) {
	// Production (no MAIL_MODE) requires MAIL_FROM.
	env := validEnv()
	delete(env, "MAIL_FROM")
	if _, err := envLoad(env); !errors.Is(err, ErrMissingRequired) {
		t.Fatalf("missing MAIL_FROM in production = %v, want ErrMissingRequired", err)
	}
	// MAIL_MODE=log needs neither MAIL_FROM nor RESEND_API_KEY.
	env["MAIL_MODE"] = "log"
	delete(env, "RESEND_API_KEY")
	if _, err := envLoad(env); err != nil {
		t.Fatalf("MAIL_MODE=log should not require MAIL_FROM/RESEND_API_KEY, got %v", err)
	}
}

func TestConfig_Secure(t *testing.T) {
	cases := map[string]bool{
		"https://guest-pass.link":   true,
		"HTTPS://guest-pass.link":   true, // scheme is case-insensitive
		"  https://guest-pass.link": true, // leading space tolerated
		"http://guest-pass.link":    false,
		"":                          false,
	}
	for base, want := range cases {
		if got := (&Config{BaseURL: base}).Secure(); got != want {
			t.Errorf("Secure(%q) = %v, want %v", base, got, want)
		}
	}
}

func TestLoad_ProductionRequiresHTTPSBaseURL(t *testing.T) {
	// Production (AUTH_MODE unset) requires an https:// BASE_URL.
	env := validEnv()
	env["BASE_URL"] = "http://guest-pass.link"
	if _, err := envLoad(env); !errors.Is(err, ErrInsecureBaseURL) {
		t.Fatalf("http BASE_URL in production = %v, want ErrInsecureBaseURL", err)
	}
	// https is fine.
	env["BASE_URL"] = "https://guest-pass.link"
	if _, err := envLoad(env); err != nil {
		t.Fatalf("https BASE_URL should load, got %v", err)
	}
}

func TestLoad_DBPathDefaultAndOverride(t *testing.T) {
	c, err := envLoad(validEnv())
	if err != nil || c.DBPath != "guestpass.db" {
		t.Fatalf("default DBPath = %q (err %v), want guestpass.db", c.DBPath, err)
	}
	env := validEnv()
	env["DB_PATH"] = "/data/guestpass.db"
	c, err = envLoad(env)
	if err != nil || c.DBPath != "/data/guestpass.db" {
		t.Fatalf("DBPath override = %q (err %v)", c.DBPath, err)
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
