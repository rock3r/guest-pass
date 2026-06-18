package config

import (
	"errors"
	"reflect"
	"testing"
	"time"
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

func TestLoad_STUNURLOptionalAndLoaded(t *testing.T) {
	// Unset: STUN-only is the default posture (D-38) but a STUN server is optional —
	// dev/loopback runs without one. Load must succeed with an empty STUNURL.
	if cfg, err := envLoad(validEnv()); err != nil {
		t.Fatalf("no STUN_URL should load fine, got %v", err)
	} else if cfg.STUNURL != "" {
		t.Errorf("STUNURL = %q, want empty", cfg.STUNURL)
	}

	env := validEnv()
	env["STUN_URL"] = "stun:stun.guest-pass.link:3478"
	cfg, err := envLoad(env)
	if err != nil {
		t.Fatalf("valid STUN_URL should load, got %v", err)
	}
	if cfg.STUNURL != "stun:stun.guest-pass.link:3478" {
		t.Errorf("STUNURL = %q, want stun:stun.guest-pass.link:3478", cfg.STUNURL)
	}
}

func TestLoad_STUNURLBadSchemeRejected(t *testing.T) {
	// A STUN_URL must use the stun:/stuns: scheme; a wrong scheme would silently produce
	// a broken ICE config, so it fails closed at startup instead.
	for _, bad := range []string{"http://stun.example.org", "stun.example.org:3478", "turn:relay.example.org", "stun:", "stuns://?foo=bar"} {
		env := validEnv()
		env["STUN_URL"] = bad
		if _, err := envLoad(env); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("STUN_URL=%q: expected ErrInvalidValue, got %v", bad, err)
		}
	}
	for _, ok := range []string{"stun:stun.example.org:3478", "stuns:stun.example.org:5349"} {
		env := validEnv()
		env["STUN_URL"] = ok
		if _, err := envLoad(env); err != nil {
			t.Errorf("STUN_URL=%q should be accepted, got %v", ok, err)
		}
	}
}

// A configured TURN_URL must use the turn:/turns: scheme; a mistyped or non-TURN URL would
// start the server as if relay were enabled while restrictive-NAT guests have no TURN path,
// so it fails closed at startup.
func TestLoad_TURNURLBadSchemeRejected(t *testing.T) {
	const turnSecret = "Tu9rNsEcReT2nQ7wL4yA8dF3gH6jK0mPqRsZxBmNc"
	// Bad scheme, or a scheme with no host (looks enabled but emits a broken ICE entry).
	for _, bad := range []string{"https://turn.example.org", "stun:relay.example.org:3478", "turn.example.org:5349", "turn:", "turns://?transport=tcp"} {
		env := validEnv()
		env["TURN_URL"] = bad
		env["TURN_SECRET"] = turnSecret
		if _, err := envLoad(env); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("TURN_URL=%q: expected ErrInvalidValue, got %v", bad, err)
		}
	}
	for _, ok := range []string{"turn:relay.example.org:3478", "turns:relay.example.org:5349"} {
		env := validEnv()
		env["TURN_URL"] = ok
		env["TURN_SECRET"] = turnSecret
		if _, err := envLoad(env); err != nil {
			t.Errorf("TURN_URL=%q should be accepted, got %v", ok, err)
		}
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

func TestLoad_PurgeDefaultsAndOverrides(t *testing.T) {
	// Defaults when unset.
	c, err := envLoad(validEnv())
	if err != nil {
		t.Fatalf("default load: %v", err)
	}
	if c.PurgeInterval != defaultPurgeInterval {
		t.Errorf("default PurgeInterval = %v, want %v", c.PurgeInterval, defaultPurgeInterval)
	}
	if c.PurgeRetention != defaultPurgeRetention {
		t.Errorf("default PurgeRetention = %v, want %v", c.PurgeRetention, defaultPurgeRetention)
	}

	// Valid overrides parse.
	env := validEnv()
	env["PURGE_INTERVAL"] = "15m"
	env["PURGE_RETENTION"] = "48h"
	c, err = envLoad(env)
	if err != nil {
		t.Fatalf("override load: %v", err)
	}
	if c.PurgeInterval != 15*time.Minute {
		t.Errorf("PurgeInterval = %v, want 15m", c.PurgeInterval)
	}
	if c.PurgeRetention != 48*time.Hour {
		t.Errorf("PurgeRetention = %v, want 48h", c.PurgeRetention)
	}
}

func TestLoad_ReapDefaultsAndOverrides(t *testing.T) {
	c, err := envLoad(validEnv())
	if err != nil {
		t.Fatalf("default load: %v", err)
	}
	if c.ReapInterval != defaultReapInterval {
		t.Errorf("default ReapInterval = %v, want %v", c.ReapInterval, defaultReapInterval)
	}
	if c.ReapIdleAfter != defaultReapIdleAfter {
		t.Errorf("default ReapIdleAfter = %v, want %v", c.ReapIdleAfter, defaultReapIdleAfter)
	}

	env := validEnv()
	env["REAP_INTERVAL"] = "2m"
	env["REAP_IDLE_AFTER"] = "30m"
	c, err = envLoad(env)
	if err != nil {
		t.Fatalf("override load: %v", err)
	}
	if c.ReapInterval != 2*time.Minute {
		t.Errorf("ReapInterval = %v, want 2m", c.ReapInterval)
	}
	if c.ReapIdleAfter != 30*time.Minute {
		t.Errorf("ReapIdleAfter = %v, want 30m", c.ReapIdleAfter)
	}
}

func TestLoad_PurgeInvalidAndNonPositiveRejected(t *testing.T) {
	cases := []struct{ name, key, val string }{
		{"unparseable interval", "PURGE_INTERVAL", "soon"},
		{"zero interval", "PURGE_INTERVAL", "0"},
		{"negative interval", "PURGE_INTERVAL", "-5m"},
		{"unparseable retention", "PURGE_RETENTION", "forever"},
		{"zero retention", "PURGE_RETENTION", "0s"},
		{"unparseable reap interval", "REAP_INTERVAL", "soon"},
		{"zero reap interval", "REAP_INTERVAL", "0"},
		{"negative reap idle", "REAP_IDLE_AFTER", "-1m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.val
			if _, err := envLoad(env); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("%s=%q: err = %v, want ErrInvalidValue", tc.key, tc.val, err)
			}
		})
	}
}
