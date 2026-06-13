package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rock3r/guest-pass/internal/store"
)

// fakeHosts is an in-memory HostStore for middleware tests.
type fakeHosts struct {
	byID map[string]*store.Host
}

func (f *fakeHosts) GetHost(_ context.Context, id string) (*store.Host, error) {
	h, ok := f.byID[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return h, nil
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// run drives a request carrying cookie through h and returns the status code.
func run(h http.Handler, cookie *http.Cookie) int {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func cookieFor(t *testing.T, ring *KeyRing, hostID string) *http.Cookie {
	t.Helper()
	tok, err := ring.Issue(hostID, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return &http.Cookie{Name: SessionCookie, Value: tok}
}

func newAuth(t *testing.T, hosts ...*store.Host) (*Authenticator, *KeyRing) {
	t.Helper()
	ring := newRing(t, testCurrent)
	byID := map[string]*store.Host{}
	for _, h := range hosts {
		byID[h.ID] = h
	}
	return NewAuthenticator(ring, &fakeHosts{byID: byID}, false), ring
}

func TestRequireHost_ActiveAllowed(t *testing.T) {
	a, ring := newAuth(t, &store.Host{ID: "h1", Status: store.HostActive})
	var gotHost *store.Host
	h := a.RequireHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, _ = HostFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	if code := run(h, cookieFor(t, ring, "h1")); code != http.StatusOK {
		t.Fatalf("active host: code = %d, want 200", code)
	}
	if gotHost == nil || gotHost.ID != "h1" {
		t.Fatalf("host not in context: %+v", gotHost)
	}
}

func TestRequireHost_RejectsByLiveStatus(t *testing.T) {
	// The DB read is live (EN-6): suspended/pending hosts are rejected mid-session even
	// with a valid token. This is the M1 DoD ("suspended host rejected by live-DB authz").
	for _, status := range []string{store.HostSuspended, store.HostPending} {
		a, ring := newAuth(t, &store.Host{ID: "h1", Status: status})
		if code := run(a.RequireHost(okHandler()), cookieFor(t, ring, "h1")); code != http.StatusForbidden {
			t.Errorf("status %q: code = %d, want 403", status, code)
		}
	}
}

func TestRequireHost_NoCookie(t *testing.T) {
	a, _ := newAuth(t, &store.Host{ID: "h1", Status: store.HostActive})
	if code := run(a.RequireHost(okHandler()), nil); code != http.StatusUnauthorized {
		t.Fatalf("no cookie: code = %d, want 401", code)
	}
}

func TestRequireHost_InvalidToken(t *testing.T) {
	a, _ := newAuth(t, &store.Host{ID: "h1", Status: store.HostActive})
	bad := &http.Cookie{Name: SessionCookie, Value: "not.a.token"}
	if code := run(a.RequireHost(okHandler()), bad); code != http.StatusUnauthorized {
		t.Fatalf("invalid token: code = %d, want 401", code)
	}
}

func TestRequireHost_ExpiredToken(t *testing.T) {
	a, ring := newAuth(t, &store.Host{ID: "h1", Status: store.HostActive})
	expired, _ := ring.issueAt("h1", time.Hour, time.Now().Add(-2*time.Hour))
	c := &http.Cookie{Name: SessionCookie, Value: expired}
	if code := run(a.RequireHost(okHandler()), c); code != http.StatusUnauthorized {
		t.Fatalf("expired token: code = %d, want 401", code)
	}
}

func TestRequireHost_DeletedHost(t *testing.T) {
	a, ring := newAuth(t) // no hosts in store
	if code := run(a.RequireHost(okHandler()), cookieFor(t, ring, "ghost")); code != http.StatusUnauthorized {
		t.Fatalf("deleted host: code = %d, want 401", code)
	}
}

func TestRequireAdmin(t *testing.T) {
	admin := &store.Host{ID: "a1", Status: store.HostActive, IsAdmin: true}
	plain := &store.Host{ID: "u1", Status: store.HostActive, IsAdmin: false}
	a, ring := newAuth(t, admin, plain)
	if code := run(a.RequireAdmin(okHandler()), cookieFor(t, ring, "a1")); code != http.StatusOK {
		t.Errorf("admin: code = %d, want 200", code)
	}
	if code := run(a.RequireAdmin(okHandler()), cookieFor(t, ring, "u1")); code != http.StatusForbidden {
		t.Errorf("non-admin: code = %d, want 403", code)
	}
}
