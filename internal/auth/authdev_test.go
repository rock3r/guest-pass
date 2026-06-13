//go:build dev

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

// Run with: go test -tags dev ./internal/auth/...
func TestDevLogin_MintsActiveAdminSessionAndReuses(t *testing.T) {
	ring := newRing(t, testCurrent)
	a := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, false)
	up := &fakeUpserter{}
	h := a.DevLogin(up, "", "")

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/auth/dev", nil))
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
	h(rec2, httptest.NewRequest(http.MethodGet, "/auth/dev", nil))
	if len(up.created) != 1 {
		t.Fatalf("dev login should reuse the dev host, created = %d", len(up.created))
	}
}
