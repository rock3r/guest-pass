//go:build dev

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

// loopbackReq is a dev-login request from localhost (DevLogin is loopback-only).
func loopbackReq() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/auth/dev", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	return r
}

// Run with: go test -tags dev ./internal/auth/...
func TestDevLogin_MintsActiveAdminSessionAndReuses(t *testing.T) {
	ring := newRing(t, testCurrent)
	a := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, false)
	up := &fakeUpserter{}
	h := a.DevLogin(up, "", "")

	rec := httptest.NewRecorder()
	h(rec, loopbackReq())
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	if len(up.created) != 1 {
		t.Fatalf("expected one dev host created, got %d", len(up.created))
	}
	if up.created[0].Status != store.HostActive || !up.created[0].IsAdmin {
		t.Errorf("dev host = %+v, want active + admin", up.created[0])
	}
	c := sessionCookieFromRec(rec)
	if c == nil {
		t.Fatal("no session cookie set by dev login")
	}
	if hid, err := ring.Verify(c.Value); err != nil || hid == "" {
		t.Fatalf("dev session does not verify: hid=%q err=%v", hid, err)
	}

	// Second login reuses the existing dev host (no duplicate create).
	rec2 := httptest.NewRecorder()
	h(rec2, loopbackReq())
	if len(up.created) != 1 {
		t.Fatalf("dev login should reuse the dev host, created = %d", len(up.created))
	}
}

func TestDevLogin_RejectsNonLoopbackClient(t *testing.T) {
	ring := newRing(t, testCurrent)
	a := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, false)
	up := &fakeUpserter{}
	h := a.DevLogin(up, "", "")

	// A LAN/remote client must not receive an admin session even on a dev binary.
	req := httptest.NewRequest(http.MethodGet, "/auth/dev", nil)
	req.RemoteAddr = "203.0.113.10:40000"
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback dev login = %d, want 403", rec.Code)
	}
	if len(up.created) != 0 || sessionCookieFromRec(rec) != nil {
		t.Fatal("non-loopback dev login must not create a host or set a session")
	}
}

// The secondary dev identity (/auth/dev?as=host2) exists ONLY to exercise cross-host
// admin flows locally (the D-27 suspend-cascade, the §7.7 metadata-only boundary, and
// the non-admin /admin 403) — none of which the single always-admin primary identity can
// reach. It must be a DISTINCT, NON-admin, active host.
func TestDevLogin_SecondIdentityIsDistinctNonAdmin(t *testing.T) {
	ring := newRing(t, testCurrent)
	a := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, false)
	up := &fakeUpserter{}
	h := a.DevLogin(up, "", "")

	// Primary identity: the existing admin dev host.
	recA := httptest.NewRecorder()
	h(recA, loopbackReq())
	if recA.Code != http.StatusFound {
		t.Fatalf("primary dev login code = %d, want 302", recA.Code)
	}

	// Secondary identity.
	req := httptest.NewRequest(http.MethodGet, "/auth/dev?as=host2", nil)
	req.RemoteAddr = "127.0.0.1:50001"
	recB := httptest.NewRecorder()
	h(recB, req)
	if recB.Code != http.StatusFound {
		t.Fatalf("secondary dev login code = %d, want 302", recB.Code)
	}

	if len(up.created) != 2 {
		t.Fatalf("expected two distinct dev hosts created, got %d", len(up.created))
	}
	primary, secondary := up.created[0], up.created[1]
	if secondary.GoogleSub == primary.GoogleSub {
		t.Errorf("secondary host reused the primary google_sub %q; want a distinct identity", secondary.GoogleSub)
	}
	if secondary.IsAdmin {
		t.Error("secondary dev host = admin; want NON-admin (it must get 403 on /admin)")
	}
	if secondary.Status != store.HostActive {
		t.Errorf("secondary dev host status = %q, want active", secondary.Status)
	}

	// The secondary session must resolve to the secondary host, not the admin.
	c := sessionCookieFromRec(recB)
	if c == nil {
		t.Fatal("no session cookie set for the secondary dev login")
	}
	hid, err := ring.Verify(c.Value)
	if err != nil || hid == "" {
		t.Fatalf("secondary dev session does not verify: hid=%q err=%v", hid, err)
	}
	if hid != "new-"+secondary.GoogleSub {
		t.Errorf("secondary session resolved to host %q, want the non-admin host new-%s", hid, secondary.GoogleSub)
	}

	// Re-login as the secondary reuses its host (no duplicate create).
	req2 := httptest.NewRequest(http.MethodGet, "/auth/dev?as=host2", nil)
	req2.RemoteAddr = "127.0.0.1:50002"
	h(httptest.NewRecorder(), req2)
	if len(up.created) != 2 {
		t.Fatalf("secondary dev login should reuse its host, created = %d", len(up.created))
	}
}
