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
