// Package token mints and verifies the opaque secret tokens GuestPass issues — magic
// links and (later) slot/host source tokens. Tokens are 128-bit random, base64url; only
// their HMAC(server_secret, token) is stored, never the raw token or a bare hash (EN-5),
// and comparison is constant-time so a leaked DB can't be ground offline and validation
// has no timing oracle.
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// Mint returns a fresh 128-bit random token, base64url (unpadded).
func Mint() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("token: generating: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Hasher computes and compares token hashes keyed by a stable server secret (EN-5).
type Hasher struct{ secret []byte }

// NewHasher builds a Hasher from the server token secret. The secret must be non-empty
// (config fails closed on a weak/empty TOKEN_SECRET upstream).
func NewHasher(secret string) (*Hasher, error) {
	if secret == "" {
		return nil, errors.New("token: empty secret")
	}
	return &Hasher{secret: []byte(secret)}, nil
}

// Hash returns the stored representation of raw: base64url(HMAC-SHA256(secret, raw)).
func (h *Hasher) Hash(raw string) string {
	return base64.RawURLEncoding.EncodeToString(h.mac(raw))
}

// Equal reports, in constant time, whether raw hashes to storedHash.
func (h *Hasher) Equal(raw, storedHash string) bool {
	want, err := base64.RawURLEncoding.DecodeString(storedHash)
	if err != nil {
		return false
	}
	return hmac.Equal(h.mac(raw), want)
}

func (h *Hasher) mac(raw string) []byte {
	m := hmac.New(sha256.New, h.secret)
	m.Write([]byte(raw))
	return m.Sum(nil)
}
