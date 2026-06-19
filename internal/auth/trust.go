package auth

import "time"

// TrustPolicy holds the progressive-trust quota dials (D-36 / §7.9 / §9.4): per-host caps that are
// tightest for new accounts and loosen once an account's age crosses TrustAfter. "Good standing" is
// already enforced upstream — a suspended host can't reach these paths at all (EN-6) — so age is the
// only dial here.
//
// Fail-safe: a zero policy enforces nothing (every quota reads as "unlimited"), so an unconfigured
// or partially-configured deployment can never lock a host out. Production wires positive defaults
// from config; tests opt in explicitly.
type TrustPolicy struct {
	TrustAfter     time.Duration // account age at which a host graduates to the trusted tier (0 = no tiering)
	NewInvites     int           // max invites sent per Window() for a new host (<=0 = unlimited)
	TrustedInvites int           // ... for a trusted host
	NewStreams     int           // max existing streams for a new host (<=0 = unlimited)
	TrustedStreams int           // ... for a trusted host
	InviteWindow   time.Duration // rolling window the invite cap is measured over (<=0 → 24h)
}

// trusted reports whether an account of the given age is in the trusted tier. Without a configured
// TrustAfter every account stays in the (possibly-unlimited) new tier.
func (p TrustPolicy) trusted(age time.Duration) bool {
	return p.TrustAfter > 0 && age >= p.TrustAfter
}

// InviteQuota returns the invite cap for an account of the given age, or <=0 for "unlimited".
func (p TrustPolicy) InviteQuota(age time.Duration) int {
	if p.trusted(age) {
		return p.TrustedInvites
	}
	return p.NewInvites
}

// StreamQuota returns the max existing-stream count for an account of the given age, or <=0 for
// "unlimited".
func (p TrustPolicy) StreamQuota(age time.Duration) int {
	if p.trusted(age) {
		return p.TrustedStreams
	}
	return p.NewStreams
}

// Window is the rolling period the invite cap is measured over (24h when unset).
func (p TrustPolicy) Window() time.Duration {
	if p.InviteWindow > 0 {
		return p.InviteWindow
	}
	return 24 * time.Hour
}
