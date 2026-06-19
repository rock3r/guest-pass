package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// adminHost creates an is_admin, active host and returns it with a session cookie.
func (a *apiHarness) adminHost(t *testing.T, sub string) (*store.Host, *http.Cookie) {
	t.Helper()
	h, err := a.store.CreateHost(context.Background(), store.CreateHostParams{
		GoogleSub: sub, Email: sub + "@example.com", Name: sub, Status: store.HostActive, IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("CreateHost(admin): %v", err)
	}
	tok, err := a.ring.Issue(h.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return h, &http.Cookie{Name: auth.SessionCookie, Value: tok}
}

// Only an is_admin host may reach the console + admin API; a regular host is forbidden and an
// anonymous request is unauthorized (authority enforced server-side via live is_admin, EN-6 / AC-9).
func TestAdmin_RequireAdmin(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "the-admin")
	_, hostCookie := a.host(t, "plain-host")

	routes := []string{"/admin", "/api/admin/stats", "/api/admin/sessions", "/api/admin/hosts"}
	for _, route := range routes {
		if rec := a.req(t, http.MethodGet, route, "", adminCookie); rec.Code != http.StatusOK {
			t.Fatalf("admin GET %s = %d, want 200", route, rec.Code)
		}
		if rec := a.req(t, http.MethodGet, route, "", hostCookie); rec.Code != http.StatusForbidden {
			t.Fatalf("non-admin GET %s = %d, want 403", route, rec.Code)
		}
		if rec := a.req(t, http.MethodGet, route, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("anon GET %s = %d, want 401", route, rec.Code)
		}
	}
}

// T-9 / §7.7 privacy boundary: the admin console shows another host's live-session + host METADATA,
// but no admin route exposes that session's guest PII, pass tokens, or any media/chat. We seed a
// foreign host with a live session and a guest carrying distinctive PII, then assert every admin
// surface (HTML + all three JSON endpoints) shows the host/stream metadata yet never leaks the guest.
func TestAdmin_PrivacyBoundary_NoForeignGuestPII(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "boundary-admin")

	// Foreign host B with a live stream + an invited guest carrying unmistakable PII.
	hostB, hostBCookie := a.host(t, "foreign-host")
	streamID := a.createStream(t, hostBCookie, "Foreign Show")
	const guestName = "Mallory Secret"
	const guestEmail = "mallory@secret.example"
	raw, err := tokenMintFor(t, a, streamID, guestName, guestEmail)
	if err != nil {
		t.Fatalf("mint guest pass: %v", err)
	}
	if _, err := a.store.StartSession(context.Background(), streamID, hostB.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// The admin DOES see the foreign session + host metadata (that's the console's purpose).
	sessionsBody := a.req(t, http.MethodGet, "/api/admin/sessions", "", adminCookie).Body.String()
	if !strings.Contains(sessionsBody, "Foreign Show") || !strings.Contains(sessionsBody, hostB.ID) {
		t.Fatalf("admin sessions missing foreign-session metadata:\n%s", sessionsBody)
	}

	// But NO admin surface leaks the foreign guest's name, email, or pass token — the values that
	// would only be reachable by reading passes or joining the room (§7.7). (We assert on these
	// concrete secrets rather than generic WebRTC keywords, which collide with the page's own
	// privacy copy that mentions "backstage chat".)
	leaks := []string{guestName, guestEmail, raw}
	for _, route := range []string{"/admin", "/api/admin/stats", "/api/admin/sessions", "/api/admin/hosts"} {
		body := a.req(t, http.MethodGet, route, "", adminCookie).Body.String()
		for _, bad := range leaks {
			if strings.Contains(body, bad) {
				t.Fatalf("admin route %s leaked %q (§7.7 privacy boundary):\n%s", route, bad, body)
			}
		}
	}
}

// The stats + sessions endpoints report accurate cross-host metadata: a live foreign session is
// counted, the TURN-relay aggregate is unavailable (no peer data recorded yet), and the hosts list
// includes every host (AC-9).
func TestAdmin_StatsAndSessions(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "stat-admin")
	hostB, hostBCookie := a.host(t, "stat-host")

	// A host whose Google subject id is distinct from its name/email, so the leak assertion is real.
	sentinel, err := a.store.CreateHost(context.Background(), store.CreateHostParams{
		GoogleSub: "GSUB-SENTINEL-do-not-expose", Email: "sentinel@example.com", Name: "Sentinel", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost(sentinel): %v", err)
	}
	streamID := a.createStream(t, hostBCookie, "Counted Show")
	if _, err := a.store.StartSession(context.Background(), streamID, hostB.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	var stats adminStatsView
	if err := json.Unmarshal(a.req(t, http.MethodGet, "/api/admin/stats", "", adminCookie).Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.LiveSessions != 1 {
		t.Fatalf("live sessions = %d, want 1", stats.LiveSessions)
	}
	if stats.TurnRelay.Available {
		t.Fatalf("turn-relay should be unavailable with no peer data, got %+v", stats.TurnRelay)
	}

	var sessions []adminSessionView
	_ = json.Unmarshal(a.req(t, http.MethodGet, "/api/admin/sessions", "", adminCookie).Body.Bytes(), &sessions)
	if len(sessions) != 1 || sessions[0].StreamTitle != "Counted Show" || sessions[0].HostID != hostB.ID {
		t.Fatalf("sessions = %+v, want one Counted Show for host B", sessions)
	}

	hostsBody := a.req(t, http.MethodGet, "/api/admin/hosts", "", adminCookie).Body.String()
	var hosts []adminHostView
	_ = json.Unmarshal([]byte(hostsBody), &hosts)
	if len(hosts) != 3 {
		t.Fatalf("hosts = %d, want 3 (admin + host B + sentinel)", len(hosts))
	}
	// The hosts list shows the sentinel's email but never its internal Google subject identifier.
	if !strings.Contains(hostsBody, "sentinel@example.com") {
		t.Fatalf("admin hosts list missing the sentinel host:\n%s", hostsBody)
	}
	if strings.Contains(hostsBody, sentinel.GoogleSub) {
		t.Fatal("admin hosts list leaked google_sub")
	}
}

// tokenMintFor mints a guest pass with explicit name/email so the privacy test can assert on
// distinctive PII.
func tokenMintFor(t *testing.T, a *apiHarness, streamID, name, email string) (string, error) {
	t.Helper()
	raw, err := token.Mint()
	if err != nil {
		return "", err
	}
	_, err = a.store.CreatePass(context.Background(), store.CreatePassParams{
		StreamID: streamID, Name: &name, Email: &email, Role: store.RoleGuest,
		TokenHash: a.hasher.Hash(raw), Status: store.PassSent,
	})
	return raw, err
}
