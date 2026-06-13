package config

import (
	"net"
	"strings"
)

// placeholderSecrets are obviously-fake values that must never authorize a boot. The
// binary fails closed (EN-14) if a required secret is empty or, case-insensitively,
// matches one of these — so a secret copied verbatim from a shipped example/compose
// file (or an obvious default) cannot reach production.
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

// isPlaceholderSecret reports whether v is empty (after trimming) or a known
// placeholder. Comparison is trim+lowercase so surrounding whitespace or casing cannot
// smuggle a placeholder past the gate.
func isPlaceholderSecret(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return true
	}
	return placeholderSecrets[v]
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
