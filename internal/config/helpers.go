package config

import (
	"net"
	"strings"
)

// minSecretLen is the minimum length (after trimming) for a fail-closed secret. The
// deployment contract generates secrets with `openssl rand -base64 48` (DEPLOYMENT
// §12.3), so any real JWT_SECRET / TURN_SECRET is far longer; this floor rejects a
// trivial non-placeholder value like `foo` that would otherwise pass the placeholder
// check yet leave the HS256 cookie / TURN HMAC material brute-forceable (EN-14 spirit:
// no silent boot on a weak secret). 32 chars admits a 128-bit hex or 192-bit base64
// key while still catching short/weak values.
const minSecretLen = 32

// placeholderSecrets are obviously-fake values that must never authorize a boot. The
// binary fails closed (EN-14) if a required secret is empty or, case-insensitively,
// matches one of these — so a secret copied verbatim from a shipped example/compose
// file (or an obvious default) cannot reach production. (Short placeholders are also
// caught by minSecretLen; this list additionally rejects any long known placeholder.)
var placeholderSecrets = map[string]bool{
	"changeme":         true,
	"change-me":        true,
	"change_me":        true,
	"placeholder":      true,
	"replace-me":       true,
	"replaceme":        true,
	"replace_me":       true,
	"secret":           true,
	"your-secret":      true,
	"your-secret-here": true,
	"your_secret_here": true,
	"example":          true,
	"todo":             true,
	"xxx":              true,
}

// isWeakSecret reports whether v is unfit to authorize a boot: empty, shorter than
// minSecretLen, or a known placeholder. Comparison is trim+lowercase so surrounding
// whitespace or casing cannot smuggle a weak value past the gate.
func isWeakSecret(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < minSecretLen {
		return true // empty or too short to be a real secret
	}
	return placeholderSecrets[strings.ToLower(v)]
}

// isLoopbackHost reports whether host is "localhost" or a loopback IP literal
// (127.0.0.0/8, ::1). host is a URL hostname with any port already stripped.
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
