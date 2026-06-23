package web

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// revealTTL bounds how long a freshly-minted magic link waits to be shown once after a
// create/re-issue redirect.
const revealTTL = 2 * time.Minute

// revealStore briefly holds a just-minted magic-link invite so the minting handler can
// POST-redirect-GET — a refresh or back/forward never re-mints a pass (Cursor Bugbot, M4
// PR-3) — while the follow-up GET still reveals the secret exactly once. It is keyed by a
// random nonce, NOT by any token, so nothing secret rides the redirect URL (no token in
// browser history or a Referer). Entries are single-use and TTL-bounded; the raw token lives
// only inside the stored value, in memory, briefly. The value is `any` to keep the store
// agnostic of the stored payload (currently the invite reveal, issuedLink).
type revealStore struct {
	mu      sync.Mutex
	entries map[string]revealRecord
}

type revealRecord struct {
	val     any
	expires time.Time
}

func newRevealStore() *revealStore {
	return &revealStore{entries: make(map[string]revealRecord)}
}

// put stores val and returns a one-time nonce to retrieve it. It opportunistically drops
// expired entries so an abandoned reveal (host never loads the redirect) can't accumulate.
func (s *revealStore) put(val any, now time.Time) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, rec := range s.entries {
		if now.After(rec.expires) {
			delete(s.entries, k)
		}
	}
	s.entries[nonce] = revealRecord{val: val, expires: now.Add(revealTTL)}
	return nonce, nil
}

// take returns and removes the entry for nonce. The entry is deleted whether or not it had
// expired (single use); ok is false for an unknown or expired nonce.
func (s *revealStore) take(nonce string, now time.Time) (any, bool) {
	if nonce == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.entries[nonce]
	if !ok {
		return nil, false
	}
	delete(s.entries, nonce)
	if now.After(rec.expires) {
		return nil, false
	}
	return rec.val, true
}
