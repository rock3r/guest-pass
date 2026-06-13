// Package auth issues and validates host sessions. The session is a hand-rolled HS256
// JWT carrying host_id ONLY (EN-6: no role/status baked in), signed by a kid two-key
// ring so JWT_SECRET rotation is a key-add, not a global logout. Every protected
// handler additionally reads hosts.status + is_admin LIVE from the DB (see middleware),
// so a suspend/approve/admin-flip takes effect mid-session without re-issuing tokens.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Token-validation errors. Callers branch with errors.Is. ErrTokenExpired is separate
// so handlers can route an expired session to a clean re-auth; everything else is
// ErrTokenInvalid (wrapping a specific cause).
var (
	ErrTokenInvalid = errors.New("auth: invalid token")
	ErrTokenExpired = errors.New("auth: token expired")
)

// KeyRing holds the active signing key plus any retired verify-only keys, each keyed by
// a kid derived from the secret. The current key signs; all keys verify (EN-6).
type KeyRing struct {
	currentKid string
	keys       map[string][]byte
}

// NewKeyRing builds a ring from the current secret (signs + verifies) and any previous
// secrets (verify-only, e.g. during rotation). Empty/blank secrets are ignored. It
// returns an error if the current secret is empty.
func NewKeyRing(current string, previous ...string) (*KeyRing, error) {
	if strings.TrimSpace(current) == "" {
		return nil, errors.New("auth: current signing key is empty")
	}
	r := &KeyRing{currentKid: kid(current), keys: map[string][]byte{}}
	r.keys[r.currentKid] = []byte(current)
	for _, p := range previous {
		if strings.TrimSpace(p) == "" {
			continue
		}
		r.keys[kid(p)] = []byte(p)
	}
	return r, nil
}

// kid derives a stable, non-secret key id from a secret (a truncated SHA-256 digest).
// It identifies which key signed a token without revealing the key.
func kid(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:16]
}

// Issue signs a host session token for hostID valid for ttl from now.
func (r *KeyRing) Issue(hostID string, ttl time.Duration) (string, error) {
	return r.issueAt(hostID, ttl, time.Now())
}

// Verify validates token and returns its host_id, or an error.
func (r *KeyRing) Verify(token string) (string, error) {
	return r.verifyAt(token, time.Now())
}

// issueAt/verifyAt take an explicit clock so tests can exercise expiry deterministically.
func (r *KeyRing) issueAt(hostID string, ttl time.Duration, now time.Time) (string, error) {
	key := r.keys[r.currentKid]
	h, err := json.Marshal(jwtHeader{Alg: "HS256", Typ: "JWT", Kid: r.currentKid})
	if err != nil {
		return "", fmt.Errorf("auth: marshaling header: %w", err)
	}
	c, err := json.Marshal(jwtClaims{HostID: hostID, Iat: now.Unix(), Exp: now.Add(ttl).Unix()})
	if err != nil {
		return "", fmt.Errorf("auth: marshaling claims: %w", err)
	}
	signingInput := b64(h) + "." + b64(c)
	return signingInput + "." + b64(sign(key, signingInput)), nil
}

func (r *KeyRing) verifyAt(token string, now time.Time) (string, error) {
	p1, p2, p3, ok := splitToken(token)
	if !ok {
		return "", fmt.Errorf("%w: malformed", ErrTokenInvalid)
	}
	hb, err := base64.RawURLEncoding.DecodeString(p1)
	if err != nil {
		return "", fmt.Errorf("%w: header encoding", ErrTokenInvalid)
	}
	var hdr jwtHeader
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return "", fmt.Errorf("%w: header json", ErrTokenInvalid)
	}
	if hdr.Alg != "HS256" { // alg-confusion guard: reject "none" and any non-HS256
		return "", fmt.Errorf("%w: unexpected alg %q", ErrTokenInvalid, hdr.Alg)
	}
	key, ok := r.keys[hdr.Kid]
	if !ok {
		return "", fmt.Errorf("%w: unknown kid", ErrTokenInvalid)
	}
	got, err := base64.RawURLEncoding.DecodeString(p3)
	if err != nil {
		return "", fmt.Errorf("%w: signature encoding", ErrTokenInvalid)
	}
	if !hmac.Equal(sign(key, p1+"."+p2), got) { // constant-time
		return "", fmt.Errorf("%w: signature mismatch", ErrTokenInvalid)
	}
	cb, err := base64.RawURLEncoding.DecodeString(p2)
	if err != nil {
		return "", fmt.Errorf("%w: claims encoding", ErrTokenInvalid)
	}
	var claims jwtClaims
	if err := json.Unmarshal(cb, &claims); err != nil {
		return "", fmt.Errorf("%w: claims json", ErrTokenInvalid)
	}
	if claims.Exp == 0 {
		return "", fmt.Errorf("%w: missing exp", ErrTokenInvalid)
	}
	if !now.Before(time.Unix(claims.Exp, 0)) {
		return "", ErrTokenExpired
	}
	if claims.HostID == "" {
		return "", fmt.Errorf("%w: missing host_id", ErrTokenInvalid)
	}
	return claims.HostID, nil
}

// sign computes the HS256 signature over signingInput with key.
func sign(key []byte, signingInput string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}

// splitToken splits a compact JWS into its three segments, rejecting any token that
// doesn't have exactly two dots.
func splitToken(token string) (header, payload, sig string, ok bool) {
	first := strings.IndexByte(token, '.')
	if first < 0 {
		return "", "", "", false
	}
	rest := token[first+1:]
	second := strings.IndexByte(rest, '.')
	if second < 0 {
		return "", "", "", false
	}
	payload = rest[:second]
	sig = rest[second+1:]
	if strings.IndexByte(sig, '.') >= 0 { // a third dot => malformed
		return "", "", "", false
	}
	return token[:first], payload, sig, true
}

// jwtHeader / jwtClaims are the wire shapes. Claims carry host_id only (EN-6).
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	HostID string `json:"host_id"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
