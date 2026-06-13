// Package config loads GuestPass configuration from environment variables into a
// single immutable Config, once, at startup (CONVENTIONS §1.5). It is the only place
// that reads os.Getenv; the rest of the code reads fields off the loaded struct.
//
// Secrets fail closed (EN-14): the binary refuses to start when JWT_SECRET — or
// TURN_SECRET, when TURN is enabled — is empty or equals a shipped placeholder. The
// AUTH_MODE=dev seam is gated behind the `dev` build tag (AD-8 / RF-4): in a release
// build setting it is a startup error, and in a dev build it additionally requires a
// loopback BASE_URL.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Sentinel errors returned (wrapped, with the offending variable's name) by Load so
// callers can branch with errors.Is without string matching (CONVENTIONS §1.2).
var (
	// ErrSecretFailClosed means a required secret was empty or a known placeholder.
	ErrSecretFailClosed = errors.New("required secret is empty or a placeholder")
	// ErrDevAuthInRelease means AUTH_MODE=dev was set in a release build, where the
	// dev-auth seam is not compiled in (AD-8 / RF-4).
	ErrDevAuthInRelease = errors.New("AUTH_MODE=dev is not permitted in a release build")
	// ErrDevBaseURLNotLoopback means a dev build was given a non-loopback BASE_URL (RF-4).
	ErrDevBaseURLNotLoopback = errors.New("AUTH_MODE=dev requires a loopback BASE_URL")
	// ErrUnknownAuthMode means AUTH_MODE was neither empty (production) nor "dev".
	ErrUnknownAuthMode = errors.New("unknown AUTH_MODE")
)

// MailMode and AuthMode constants are the only accepted non-default values.
const (
	MailModeLog = "log"
	AuthModeDev = "dev"
)

// Config is the loaded, validated, immutable configuration. Construct it only via
// Load; do not mutate it after load (CONVENTIONS §1.5).
type Config struct {
	BaseURL            string
	GoogleClientID     string
	GoogleClientSecret string
	JWTSecret          string
	ResendAPIKey       string
	MailMode           string // "" => Resend (default); "log" => print magic links to stdout (D-2)
	AdminEmail         string
	SignupMode         string // open | approval | allowlist (§9.3)
	TURNURL            string
	TURNSecret         string
	AllowedHosts       []string
	CodecOptin         []string
	AuthMode           string // "" => production (Google OAuth); "dev" => fake host session (AD-8)
}

// TURNEnabled reports whether a TURN relay is configured. When false the deployment is
// STUN-only (D-38) and TURN_SECRET is not required.
func (c *Config) TURNEnabled() bool { return strings.TrimSpace(c.TURNURL) != "" }

// Load reads configuration from the process environment and validates it, returning a
// fail-closed error (EN-14 / AD-8) rather than a partially-valid Config.
func Load() (*Config, error) { return load(os.Getenv) }

// load takes a getenv seam so tests can drive it without mutating the process
// environment.
func load(getenv func(string) string) (*Config, error) {
	c := &Config{
		BaseURL:            strings.TrimSpace(getenv("BASE_URL")),
		GoogleClientID:     strings.TrimSpace(getenv("GOOGLE_CLIENT_ID")),
		GoogleClientSecret: getenv("GOOGLE_CLIENT_SECRET"),
		JWTSecret:          getenv("JWT_SECRET"),
		ResendAPIKey:       getenv("RESEND_API_KEY"),
		MailMode:           strings.TrimSpace(getenv("MAIL_MODE")),
		AdminEmail:         strings.TrimSpace(getenv("ADMIN_EMAIL")),
		SignupMode:         strings.TrimSpace(getenv("SIGNUP_MODE")),
		TURNURL:            strings.TrimSpace(getenv("TURN_URL")),
		TURNSecret:         getenv("TURN_SECRET"),
		AllowedHosts:       splitList(getenv("ALLOWED_HOSTS")),
		CodecOptin:         splitList(getenv("CODEC_OPTIN")),
		AuthMode:           strings.TrimSpace(getenv("AUTH_MODE")),
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// validate enforces the step-1 fail-closed invariants. Additional required-var checks
// land with the consumers that need them.
func (c *Config) validate() error {
	// JWT_SECRET is always required and fails closed (EN-14).
	if isPlaceholderSecret(c.JWTSecret) {
		return fmt.Errorf("config: JWT_SECRET: %w", ErrSecretFailClosed)
	}
	// TURN_SECRET fails closed only when a TURN relay is configured; STUN-only
	// deployments (D-38) do not require it.
	if c.TURNEnabled() && isPlaceholderSecret(c.TURNSecret) {
		return fmt.Errorf("config: TURN_SECRET: %w", ErrSecretFailClosed)
	}
	if err := c.validateAuthMode(); err != nil {
		return err
	}
	return nil
}

// validateAuthMode enforces the AUTH_MODE seam (AD-8 / RF-4): only "" (production) and
// "dev" are accepted; "dev" is permitted only in a dev build, where it additionally
// requires a loopback BASE_URL.
func (c *Config) validateAuthMode() error {
	switch c.AuthMode {
	case "":
		return nil
	case AuthModeDev:
		if !devBuild {
			return fmt.Errorf("config: AUTH_MODE=%s: %w", c.AuthMode, ErrDevAuthInRelease)
		}
		if !isLoopbackURL(c.BaseURL) {
			return fmt.Errorf("config: BASE_URL %q: %w", c.BaseURL, ErrDevBaseURLNotLoopback)
		}
		return nil
	default:
		return fmt.Errorf("config: AUTH_MODE=%s: %w", c.AuthMode, ErrUnknownAuthMode)
	}
}

// splitList parses a comma-separated env value into a trimmed, non-empty slice.
func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isLoopbackURL reports whether the URL's host is a loopback address or "localhost".
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return isLoopbackHost(u.Hostname())
}
