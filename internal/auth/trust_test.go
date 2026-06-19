package auth

import (
	"testing"
	"time"
)

func TestTrustPolicy_Tiers(t *testing.T) {
	p := TrustPolicy{
		TrustAfter: 7 * 24 * time.Hour,
		NewInvites: 10, TrustedInvites: 100,
		NewStreams: 3, TrustedStreams: 50,
	}
	newAge := 1 * time.Hour
	trustedAge := 8 * 24 * time.Hour
	if got := p.InviteQuota(newAge); got != 10 {
		t.Fatalf("new invite quota = %d, want 10", got)
	}
	if got := p.InviteQuota(trustedAge); got != 100 {
		t.Fatalf("trusted invite quota = %d, want 100", got)
	}
	if got := p.StreamQuota(newAge); got != 3 {
		t.Fatalf("new stream quota = %d, want 3", got)
	}
	if got := p.StreamQuota(trustedAge); got != 50 {
		t.Fatalf("trusted stream quota = %d, want 50", got)
	}
	// Exactly at the threshold is already trusted.
	if got := p.InviteQuota(7 * 24 * time.Hour); got != 100 {
		t.Fatalf("at-threshold invite quota = %d, want 100 (trusted)", got)
	}
	if p.Window() != 24*time.Hour {
		t.Fatalf("default window = %v, want 24h", p.Window())
	}
}

// A zero policy enforces nothing — every quota is "unlimited" (<=0), so an unconfigured deployment
// never locks a host out (fail-safe).
func TestTrustPolicy_ZeroIsUnlimited(t *testing.T) {
	var p TrustPolicy
	if p.InviteQuota(0) > 0 || p.InviteQuota(99*24*time.Hour) > 0 {
		t.Fatal("zero policy must yield unlimited (<=0) invite quota")
	}
	if p.StreamQuota(0) > 0 {
		t.Fatal("zero policy must yield unlimited (<=0) stream quota")
	}
}
