package web

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// overStreamQuota reads the account age and applies the right tier: a brand-new host is held to the
// new-tier cap, an aged host to the (looser) trusted-tier cap (D-36). A zero policy is unlimited.
func TestOverStreamQuota_AgeTiers(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h, _ := st.CreateHost(ctx, store.CreateHostParams{GoogleSub: "g", Email: "h@example.com", Name: "H", Status: store.HostActive})
	for i := 0; i < 2; i++ {
		if _, err := st.CreateStream(ctx, store.CreateStreamParams{HostID: h.ID, Title: "S"}); err != nil {
			t.Fatalf("CreateStream: %v", err)
		}
	}
	now := time.Unix(2_000_000_000, 0)
	policy := auth.TrustPolicy{TrustAfter: 7 * 24 * time.Hour, NewStreams: 2, TrustedStreams: 10}

	// New account (age 1h): 2 streams == the new cap → over.
	newHost := &store.Host{ID: h.ID, CreatedAt: now.Add(-time.Hour).Unix()}
	if over, limit := overStreamQuota(ctx, st, policy, newHost, now); !over || limit != 2 {
		t.Fatalf("new host overStreamQuota = (%v,%d), want (true,2)", over, limit)
	}
	// Aged account (age 8d): 2 streams < the trusted cap → not over.
	agedHost := &store.Host{ID: h.ID, CreatedAt: now.Add(-8 * 24 * time.Hour).Unix()}
	if over, limit := overStreamQuota(ctx, st, policy, agedHost, now); over || limit != 10 {
		t.Fatalf("aged host overStreamQuota = (%v,%d), want (false,10)", over, limit)
	}
	// Zero policy: never over.
	if over, _ := overStreamQuota(ctx, st, auth.TrustPolicy{}, newHost, now); over {
		t.Fatal("zero policy must never be over quota (unlimited)")
	}
}

// The invite cap is enforced over HTTP: a new host can send up to the cap, then is rejected (PRG to
// the stream detail with the quota flash), and the over-cap invite never created a pass (D-36).
func TestQuota_InviteCapEnforcedOverHTTP(t *testing.T) {
	a := newAPIHarnessTrust(t, auth.TrustPolicy{TrustAfter: time.Hour, NewInvites: 2, TrustedInvites: 50})
	_, cookie := a.host(t, "invite-capped") // fresh account → new tier
	streamID := a.createStream(t, cookie, "Show")

	for i, email := range []string{"a@example.com", "b@example.com"} {
		rec := a.formPost(t, "/app/streams/"+streamID+"/passes", "email="+email+"&role=guest", cookie)
		if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "error=invite-quota") {
			t.Fatalf("invite %d = %d loc=%q, want a successful 303 (under cap)", i+1, rec.Code, rec.Header().Get("Location"))
		}
	}
	// The 3rd invite is over the cap.
	rec := a.formPost(t, "/app/streams/"+streamID+"/passes", "email=c@example.com&role=guest", cookie)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=invite-quota") {
		t.Fatalf("over-cap invite = %d loc=%q, want 303 → error=invite-quota", rec.Code, rec.Header().Get("Location"))
	}
	// Exactly the two under-cap invites exist for the host.
	n, err := a.store.CountInvitesSentByHost(context.Background(), mustHostID(t, a, streamID), time.Now().Add(-24*time.Hour).Unix())
	if err != nil || n != 2 {
		t.Fatalf("sent invites = (%d,%v), want (2,nil) — the over-cap invite must not have been sent", n, err)
	}
}

// The concurrent-stream cap is enforced over the JSON API: a new host can create up to the cap, then
// gets 429 (D-36).
func TestQuota_StreamCapEnforcedOverHTTP(t *testing.T) {
	a := newAPIHarnessTrust(t, auth.TrustPolicy{TrustAfter: time.Hour, NewStreams: 2, TrustedStreams: 50})
	_, cookie := a.host(t, "stream-capped")

	for i := 0; i < 2; i++ {
		if rec := a.req(t, http.MethodPost, "/api/streams", `{"title":"S"}`, cookie); rec.Code != http.StatusCreated {
			t.Fatalf("stream %d = %d, want 201 (under cap)", i+1, rec.Code)
		}
	}
	if rec := a.req(t, http.MethodPost, "/api/streams", `{"title":"S3"}`, cookie); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-cap stream = %d, want 429", rec.Code)
	}
}

// Per-IP throttle (token-scanning defense, §7.9): a tight limiter 429s a rapid second GET /p/{token}
// from the same client.
func TestQuota_PerIPTokenThrottle(t *testing.T) {
	a := newAPIHarnessRL(t, NewRateLimiter(1, 1)) // 1/s sustained, burst 1
	_, cookie := a.host(t, "throttle-host")
	streamID := a.createStream(t, cookie, "Show")
	_, raw := a.mintPass(t, streamID, "Dana")

	if rec := a.req(t, http.MethodGet, "/p/"+raw, "", nil); rec.Code == http.StatusTooManyRequests {
		t.Fatalf("first GET /p was throttled (%d), want it to pass", rec.Code)
	}
	if rec := a.req(t, http.MethodGet, "/p/"+raw, "", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rapid second GET /p = %d, want 429 (per-IP throttle)", rec.Code)
	}
}

func mustHostID(t *testing.T, a *apiHarness, streamID string) string {
	t.Helper()
	s, err := a.store.GetStream(context.Background(), streamID)
	if err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	return s.HostID
}

// Re-issue is subject to the same invite cap as a fresh invite (D-36): once a host is at their
// distinct-invite cap, re-issuing an existing pass is refused too — it can't be a quota bypass.
func TestQuota_ReissueRespectsInviteCap(t *testing.T) {
	a := newAPIHarnessTrust(t, auth.TrustPolicy{TrustAfter: time.Hour, NewInvites: 1, TrustedInvites: 50})
	_, cookie := a.host(t, "reissue-capped")
	streamID := a.createStream(t, cookie, "Show")

	// One invite reaches the new-tier cap of 1.
	if rec := a.formPost(t, "/app/streams/"+streamID+"/passes", "email=a@example.com&role=guest", cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("first invite = %d, want 303", rec.Code)
	}
	passes, err := a.store.ListPassesByStream(context.Background(), streamID)
	if err != nil || len(passes) != 1 {
		t.Fatalf("ListPassesByStream = (%d,%v), want 1 pass", len(passes), err)
	}
	// Re-issuing that pass is over the cap → refused (PRG with the quota flash), not a bypass.
	rec := a.formPost(t, "/app/streams/"+streamID+"/passes/"+passes[0].ID+"/reissue", "", cookie)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=invite-quota") {
		t.Fatalf("over-cap reissue = %d loc=%q, want 303 → error=invite-quota", rec.Code, rec.Header().Get("Location"))
	}
}

// The per-host invite send-rate limiter throttles a burst of email-sending actions from one host —
// bounding re-send volume that the per-window count alone can't (D-36 §7.9).
func TestQuota_InviteSendRateLimited(t *testing.T) {
	a := newAPIHarnessInviteLimiter(t, NewRateLimiter(0.0001, 2)) // burst 2, effectively no refill
	_, cookie := a.host(t, "rate-host")
	streamID := a.createStream(t, cookie, "Show")

	// The first two invite-sends consume the burst.
	for i := 0; i < 2; i++ {
		if rec := a.formPost(t, "/app/streams/"+streamID+"/passes", "email=g@example.com&role=guest", cookie); rec.Code != http.StatusSeeOther {
			t.Fatalf("invite %d = %d, want 303 (within burst)", i+1, rec.Code)
		}
	}
	// The third email-sending action — even a re-issue — is throttled to 429.
	passes, _ := a.store.ListPassesByStream(context.Background(), streamID)
	if rec := a.formPost(t, "/app/streams/"+streamID+"/passes/"+passes[0].ID+"/reissue", "", cookie); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("post-burst reissue = %d, want 429 (per-host send-rate limit)", rec.Code)
	}
}
