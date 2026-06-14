package store

import (
	"crypto/rand"
	"fmt"
)

// Domain entity types. IDs are UUIDv4 TEXT; timestamps are INTEGER Unix-seconds UTC
// (AD-17). NULLable columns are pointer fields. Token columns hold HMAC(secret, token)
// computed by the caller (EN-5) — the store never sees a raw token or computes crypto.

// Enumerated column values, matching the CHECK constraints in migration 0001.
const (
	HostPending   = "pending"
	HostActive    = "active"
	HostSuspended = "suspended"

	StreamDraft     = "draft"
	StreamScheduled = "scheduled"
	StreamLive      = "live"
	StreamEnded     = "ended"

	PassCreated  = "created"
	PassSent     = "sent"
	PassOpened   = "opened"
	PassAccepted = "accepted"
	PassExpired  = "expired"
	PassRevoked  = "revoked"

	RoleGuest  = "guest"
	RoleCohost = "cohost"

	SlotCam         = "cam"
	SlotHost        = "host"
	SlotScreenshare = "screenshare"
)

// Host is a Google-authenticated host (D-28 status lifecycle).
type Host struct {
	ID        string
	GoogleSub string
	Email     string
	Name      string
	Picture   *string
	IsAdmin   bool
	Status    string // pending | active | suspended
	CreatedAt int64
}

// Stream is a host's planned/live show (D-19 quality ceiling, D-29 linked channel).
type Stream struct {
	ID               string
	HostID           string
	Title            string
	ScheduledAt      *int64
	DurationMin      *int64
	Status           string // draft | scheduled | live | ended
	MaxRes           *int64
	MaxFPS           *int64
	MaxBitrateKbps   *int64
	TwitchYTChannel  *string
	TwitchYTPlatform *string // twitch | youtube
	CreatedAt        int64
}

// Slot is a host-global OBS source slot, wired into OBS once (D-20).
type Slot struct {
	ID                      string
	HostID                  string
	Kind                    string // cam | host | screenshare
	Idx                     *int64 // cam slots 1..8; NULL for host/screenshare
	SourceTokenHash         string // HMAC(secret, token) (EN-5)
	SourceTokenLastUsedAt   *int64
	SourceTokenLastSourceIP *string
	Epoch                   int64
}

// Pass is a guest magic-link invite (D-37 PII, D-20 slot binding).
type Pass struct {
	ID         string
	StreamID   string
	SlotID     *string
	Name       *string // guest PII, purged 24h post-stream (D-37)
	Email      *string // guest PII, purged 24h post-stream (D-37)
	Role       string  // guest | cohost
	TokenHash  string  // HMAC(secret, token) (EN-5)
	CanScreen  bool
	Status     string // created | sent | opened | accepted | expired | revoked
	SentAt     *int64
	ExpiresAt  *int64
	OpenedAt   *int64
	AcceptedAt *int64
	RevokedAt  *int64
}

// PassLock is a persisted suppression lock (AD-22/D-13): one row per (pass, modality) that
// re-applies on room (re)spawn, so a force-muted guest stays muted across a restart. The
// applier_pass_id is NULL when the host applied it (the host has no pass); the rank floor
// still distinguishes a host- from a cohost-applied lock.
type PassLock struct {
	PassID           string
	Modality         string  // mic | cam | share
	ApplierRankFloor string  // host | cohost
	ApplierPassID    *string // NULL = applied by the host (no pass)
	CreatedAt        int64
}

// newID returns a random UUIDv4 string. crypto/rand failure is unrecoverable, so it is
// surfaced as an error rather than silently producing a weak ID.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
