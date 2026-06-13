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
	// ErrSecretFailClosed means a required secret was empty, too short, or a known
	// placeholder (EN-14).
	ErrSecretFailClosed = errors.New("required secret is empty, too short, or a placeholder")
	// ErrDevAuthInRelease means AUTH_MODE=dev was set in a release build, where the
	// dev-auth seam is not compiled in (AD-8 / RF-4).
	ErrDevAuthInRelease = errors.New("AUTH_MODE=dev is not permitted in a release build")
	// ErrDevBaseURLNotLoopback means a dev build was given a non-loopback BASE_URL (RF-4).
	ErrDevBaseURLNotLoopback = errors.New("AUTH_MODE=dev requires a loopback BASE_URL")
	// ErrUnknownAuthMode means AUTH_MODE was neither empty (production) nor "dev".
	ErrUnknownAuthMode = errors.New("unknown AUTH_MODE")
	// ErrMissingRequired means a required configuration variable was empty
	// (CONVENTIONS §1.5: a missing required var is a startup error).
	ErrMissingRequired = errors.New("required configuration is missing")
	// ErrInvalidValue means a variable held a value outside its accepted set
	// (e.g. an unknown SIGNUP_MODE or MAIL_MODE).
	ErrInvalidValue = errors.New("invalid configuration value")
	// ErrInsecureBaseURL means BASE_URL is not https:// outside dev. Production requires
	// HTTPS — Secure cookies, WebRTC, and Google OAuth all depend on it (DEPLOYMENT §2).
	ErrInsecureBaseURL = errors.New("BASE_URL must be https:// outside dev")
)

// Accepted non-default values for the small enum-valued variables.
const (
	MailModeLog = "log"
	AuthModeDev = "dev"

	SignupModeOpen      = "open"
	SignupModeApproval  = "approval"
	SignupModeAllowlist = "allowlist"
)

// Config is the loaded, validated, immutable configuration. Construct it only via
// Load; do not mutate it after load (CONVENTIONS §1.5).
type Config struct {
	BaseURL            string
	GoogleClientID     string
	GoogleClientSecret string
	JWTSecret          string
	JWTSecretPrevious  string // optional verify-only second key in the kid ring (EN-6); set during rotation
	ResendAPIKey       string
	MailMode           string // "" => Resend (default); "log" => print magic links to stdout (D-2)
	AdminEmail         string
	SignupMode         string // open | approval | allowlist (§9.3)
	TURNURL            string
	TURNSecret         string
	AllowedHosts       []string
	CodecOptin         []string
	AuthMode           string // "" => production (Google OAuth); "dev" => fake host session (AD-8)
	DBPath             string // SQLite file path (DB_PATH); defaults to guestpass.db
}

// defaultDBPath is used when DB_PATH is unset; docker-compose overrides it to the
// mounted volume (DEPLOYMENT §6).
const defaultDBPath = "guestpass.db"

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
		JWTSecretPrevious:  getenv("JWT_SECRET_PREVIOUS"),
		ResendAPIKey:       getenv("RESEND_API_KEY"),
		MailMode:           strings.TrimSpace(getenv("MAIL_MODE")),
		AdminEmail:         strings.TrimSpace(getenv("ADMIN_EMAIL")),
		SignupMode:         strings.TrimSpace(getenv("SIGNUP_MODE")),
		TURNURL:            strings.TrimSpace(getenv("TURN_URL")),
		TURNSecret:         getenv("TURN_SECRET"),
		AllowedHosts:       splitList(getenv("ALLOWED_HOSTS")),
		CodecOptin:         splitList(getenv("CODEC_OPTIN")),
		AuthMode:           strings.TrimSpace(getenv("AUTH_MODE")),
		DBPath:             strings.TrimSpace(getenv("DB_PATH")),
	}
	if c.DBPath == "" {
		c.DBPath = defaultDBPath
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
	if isWeakSecret(c.JWTSecret) {
		return fmt.Errorf("config: JWT_SECRET: %w", ErrSecretFailClosed)
	}
	// JWT_SECRET_PREVIOUS is optional (the verify-only second key during rotation, EN-6),
	// but when set it must be a real secret too.
	if strings.TrimSpace(c.JWTSecretPrevious) != "" && isWeakSecret(c.JWTSecretPrevious) {
		return fmt.Errorf("config: JWT_SECRET_PREVIOUS: %w", ErrSecretFailClosed)
	}
	// TURN_SECRET fails closed only when a TURN relay is configured; STUN-only
	// deployments (D-38) do not require it.
	if c.TURNEnabled() && isWeakSecret(c.TURNSecret) {
		return fmt.Errorf("config: TURN_SECRET: %w", ErrSecretFailClosed)
	}
	if err := c.validateAuthMode(); err != nil {
		return err
	}
	if err := c.validateMailMode(); err != nil {
		return err
	}
	if err := c.validateRequired(); err != nil {
		return err
	}
	if err := c.validateBaseURLScheme(); err != nil {
		return err
	}
	if err := c.validateSignupMode(); err != nil {
		return err
	}
	return nil
}

// validateRequired enforces that required variables are present (CONVENTIONS §1.5:
// a missing required var is a startup error, not a runtime surprise; DEPLOYMENT §3).
// Two conditional exemptions: Google OAuth credentials are not required in a dev build
// (AUTH_MODE=dev mints a fake session without Google, AD-8), and RESEND_API_KEY is not
// required when MAIL_MODE=log prints magic links to stdout (D-2).
func (c *Config) validateRequired() error {
	required := []struct{ name, val string }{
		{"BASE_URL", c.BaseURL},
		{"ADMIN_EMAIL", c.AdminEmail},
		{"SIGNUP_MODE", c.SignupMode},
	}
	if c.AuthMode != AuthModeDev {
		required = append(required,
			struct{ name, val string }{"GOOGLE_CLIENT_ID", c.GoogleClientID},
			struct{ name, val string }{"GOOGLE_CLIENT_SECRET", c.GoogleClientSecret},
		)
	}
	if c.MailMode != MailModeLog {
		required = append(required, struct{ name, val string }{"RESEND_API_KEY", c.ResendAPIKey})
	}
	for _, r := range required {
		if strings.TrimSpace(r.val) == "" {
			return fmt.Errorf("config: %s: %w", r.name, ErrMissingRequired)
		}
	}
	return nil
}

// validateBaseURLScheme requires an https:// BASE_URL outside dev. Production needs
// HTTPS for Secure cookies, WebRTC, and Google OAuth (DEPLOYMENT §2); a dev build with
// AUTH_MODE=dev uses a loopback http origin instead (enforced in validateAuthMode). It
// runs after validateRequired, so an empty BASE_URL is reported as missing, not insecure.
func (c *Config) validateBaseURLScheme() error {
	if c.AuthMode == AuthModeDev {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.BaseURL)), "https://") {
		return fmt.Errorf("config: BASE_URL %q: %w", c.BaseURL, ErrInsecureBaseURL)
	}
	return nil
}

// validateMailMode accepts only "" (Resend, default) or "log" (D-2).
func (c *Config) validateMailMode() error {
	switch c.MailMode {
	case "", MailModeLog:
		return nil
	default:
		return fmt.Errorf("config: MAIL_MODE=%s: %w", c.MailMode, ErrInvalidValue)
	}
}

// validateSignupMode requires one of the three onboarding modes (§9.3). An empty value
// is caught earlier by validateRequired as missing; a non-empty unknown value is invalid.
func (c *Config) validateSignupMode() error {
	switch c.SignupMode {
	case SignupModeOpen, SignupModeApproval, SignupModeAllowlist:
		return nil
	default:
		return fmt.Errorf("config: SIGNUP_MODE=%s: %w", c.SignupMode, ErrInvalidValue)
	}
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
