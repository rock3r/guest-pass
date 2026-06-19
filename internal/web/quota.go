package web

import (
	"context"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// hostAge is how long ago the account was created, as of now.
func hostAge(host *store.Host, now time.Time) time.Duration {
	return time.Duration(now.Unix()-host.CreatedAt) * time.Second
}

// overInviteQuota reports whether the host has reached their progressive-trust invite cap (D-36) and
// the cap that applied. A cap of <=0 means unlimited (policy disabled / trusted-unbounded). It FAILS
// OPEN on a count error: a transient DB hiccup must never lock a legitimate host out of inviting — the
// per-IP limiter + abuse reporting + suspend remain as backstops.
func overInviteQuota(ctx context.Context, st *store.Store, p auth.TrustPolicy, host *store.Host, now time.Time) (over bool, limit int) {
	limit = p.InviteQuota(hostAge(host, now))
	if limit <= 0 {
		return false, 0
	}
	n, err := st.CountInvitesSentByHost(ctx, host.ID, now.Add(-p.Window()).Unix())
	if err != nil {
		return false, limit
	}
	return n >= limit, limit
}

// overStreamQuota reports whether the host has reached their progressive-trust concurrent-stream cap
// (D-36) and the cap that applied. Same <=0-is-unlimited + fail-open semantics as overInviteQuota.
func overStreamQuota(ctx context.Context, st *store.Store, p auth.TrustPolicy, host *store.Host, now time.Time) (over bool, limit int) {
	limit = p.StreamQuota(hostAge(host, now))
	if limit <= 0 {
		return false, 0
	}
	n, err := st.CountStreamsByHost(ctx, host.ID)
	if err != nil {
		return false, limit
	}
	return n >= limit, limit
}
