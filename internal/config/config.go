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
	"time"
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
	TokenSecret        string // STABLE HMAC key for magic-link/slot/host token hashing (EN-5)
	ResendAPIKey       string
	MailMode           string // "" => Resend (default); "log" => print magic links to stdout (D-2)
	MailFrom           string // From address for Resend invites; required unless MAIL_MODE=log
	AdminEmail         string
	SignupMode         string // open | approval | allowlist (§9.3)
	STUNURL            string // stun:/stuns: URL offered to every peer in the ICE config (D-38); optional
	TURNURL            string
	TURNSecret         string
	AllowedHosts       []string
	CodecOptin         []string
	AuthMode           string // "" => production (Google OAuth); "dev" => fake host session (AD-8)
	DBPath             string // SQLite file path (DB_PATH); defaults to guestpass.db

	// Background-job dials (DESIGN §9.7 / D-37 / D-40), config-backed with safe defaults.
	PurgeInterval   time.Duration // PURGE_INTERVAL: how often the guest-PII purge sweeps
	PurgeRetention  time.Duration // PURGE_RETENTION: how long guest PII is kept after stream end
	ReportRetention time.Duration // REPORT_RETENTION: how long an abuse report's reporter/message is kept before anonymizing (D-42)
	ReapInterval    time.Duration // REAP_INTERVAL: how often the idle-session reaper polls
	ReapIdleAfter   time.Duration // REAP_IDLE_AFTER: end a session idle (no participants) this long
}

// defaultDBPath is used when DB_PATH is unset; docker-compose overrides it to the
// mounted volume (DEPLOYMENT §6).
const defaultDBPath = "guestpass.db"

// Default background-job dials (DESIGN §9.7). DEPLOYMENT §8 documents PURGE_INTERVAL /
// PURGE_RETENTION as the overrides.
const (
	defaultPurgeInterval   = time.Hour           // "hourly sweep"
	defaultPurgeRetention  = 24 * time.Hour      // 24h guest-PII retention (D-37)
	defaultReportRetention = 30 * 24 * time.Hour // abuse-report review window before anonymizing (D-42)
	defaultReapInterval    = time.Minute         // idle-session reaper poll cadence (D-40)
	defaultReapIdleAfter   = 15 * time.Minute    // auto-end a session idle this long (frees the slot)
)

// TURNEnabled reports whether a TURN relay is configured. When false the deployment is
// STUN-only (D-38) and TURN_SECRET is not required.
func (c *Config) TURNEnabled() bool { return strings.TrimSpace(c.TURNURL) != "" }

// Secure reports whether BASE_URL is an https origin, matching the scheme normalization
// the validator uses (case- and space-insensitive). It is the single source of truth
// for the session-cookie Secure flag and the CSP ws/wss scheme, so a "HTTPS://" or
// space-padded value can't desync cookie security from validation.
func (c *Config) Secure() bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.BaseURL)), "https://")
}

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
		TokenSecret:        getenv("TOKEN_SECRET"),
		ResendAPIKey:       getenv("RESEND_API_KEY"),
		MailMode:           strings.TrimSpace(getenv("MAIL_MODE")),
		MailFrom:           strings.TrimSpace(getenv("MAIL_FROM")),
		AdminEmail:         strings.TrimSpace(getenv("ADMIN_EMAIL")),
		SignupMode:         strings.TrimSpace(getenv("SIGNUP_MODE")),
		STUNURL:            strings.TrimSpace(getenv("STUN_URL")),
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
	var err error
	if c.PurgeInterval, err = parsePositiveDuration("PURGE_INTERVAL", getenv("PURGE_INTERVAL"), defaultPurgeInterval); err != nil {
		return nil, err
	}
	if c.PurgeRetention, err = parsePositiveDuration("PURGE_RETENTION", getenv("PURGE_RETENTION"), defaultPurgeRetention); err != nil {
		return nil, err
	}
	if c.ReportRetention, err = parsePositiveDuration("REPORT_RETENTION", getenv("REPORT_RETENTION"), defaultReportRetention); err != nil {
		return nil, err
	}
	if c.ReapInterval, err = parsePositiveDuration("REAP_INTERVAL", getenv("REAP_INTERVAL"), defaultReapInterval); err != nil {
		return nil, err
	}
	if c.ReapIdleAfter, err = parsePositiveDuration("REAP_IDLE_AFTER", getenv("REAP_IDLE_AFTER"), defaultReapIdleAfter); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// parsePositiveDuration parses an optional Go duration env var, returning def when unset and
// failing closed (ErrInvalidValue) on an unparseable or non-positive value — a zero/negative
// purge interval or retention would disable or corrupt the retention guarantee (D-37).
func parsePositiveDuration(name, raw string, def time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q: %w", name, raw, ErrInvalidValue)
	}
	if d <= 0 {
		return 0, fmt.Errorf("config: %s=%q must be positive: %w", name, raw, ErrInvalidValue)
	}
	return d, nil
}

// validate enforces the step-1 fail-closed invariants. Additional required-var checks
// land with the consumers that need them.
func (c *Config) validate() error {
	// JWT_SECRET is always required and fails closed (EN-14).
	if isWeakSecret(c.JWTSecret) {
		return fmt.Errorf("config: JWT_SECRET: %w", ErrSecretFailClosed)
	}
	// TOKEN_SECRET keys the HMAC of magic-link/slot/host tokens (EN-5). It is a STABLE
	// secret, separate from JWT_SECRET (which rotates via the kid ring, EN-6) — reusing
	// the rotating key would orphan every stored token hash on rotation.
	if isWeakSecret(c.TokenSecret) {
		return fmt.Errorf("config: TOKEN_SECRET: %w", ErrSecretFailClosed)
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
	if err := c.validateSTUNURL(); err != nil {
		return err
	}
	if err := c.validateTURNURL(); err != nil {
		return err
	}
	return nil
}

// validateSTUNURL requires the optional STUN_URL, when set, to be a usable stun:/stuns:
// URL. Empty is fine — the deployment then offers no STUN server (dev/loopback).
func (c *Config) validateSTUNURL() error {
	return validateICEURL("STUN_URL", c.STUNURL, []string{"stun:", "stuns:"})
}

// validateTURNURL requires a configured TURN_URL to be a usable turn:/turns: URL. A
// mistyped, non-TURN, or host-less URL would let the server start as if relay were enabled
// while restrictive-NAT guests still have no TURN path, so it fails closed at startup.
// Empty is fine — the deployment is then STUN-only (D-38).
func (c *Config) validateTURNURL() error {
	return validateICEURL("TURN_URL", c.TURNURL, []string{"turn:", "turns:"})
}

// validateICEURL fails closed on an ICE server URL that is non-empty but unusable: it must
// start with one of the allowed schemes AND carry a non-empty host. A scheme-only value
// like "turn:" or "turns://?transport=tcp" would otherwise look enabled while emitting a
// broken ICE entry. An empty raw value is accepted (the server is unconfigured).
func validateICEURL(name, raw string, schemes []string) error {
	if raw == "" {
		return nil
	}
	rest, matched := "", false
	for _, sc := range schemes {
		if strings.HasPrefix(raw, sc) {
			rest, matched = raw[len(sc):], true
			break
		}
	}
	if !matched {
		return fmt.Errorf("config: %s=%q must use a %s scheme: %w", name, raw, strings.Join(schemes, "/"), ErrInvalidValue)
	}
	// Strip an optional "//", the query, and any :port // /path, leaving the host.
	rest = strings.TrimPrefix(rest, "//")
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.IndexAny(rest, ":/"); i >= 0 {
		rest = rest[:i]
	}
	if strings.TrimSpace(rest) == "" {
		return fmt.Errorf("config: %s=%q has no host: %w", name, raw, ErrInvalidValue)
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
		// Real mail delivery (Resend) needs an API key and a verified From address; the
		// log mode (D-2) prints magic links to stdout and needs neither.
		required = append(required,
			struct{ name, val string }{"RESEND_API_KEY", c.ResendAPIKey},
			struct{ name, val string }{"MAIL_FROM", c.MailFrom},
		)
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
	if !c.Secure() {
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
