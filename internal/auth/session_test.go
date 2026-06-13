package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

func sessionCookieFromRec(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	return nil
}

func TestSetSession_CookieAttributesAndRoundTrip(t *testing.T) {
	ring := newRing(t, testCurrent)
	a := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, true)

	rec := httptest.NewRecorder()
	if err := a.SetSession(rec, "host-7"); err != nil {
		t.Fatalf("SetSession: %v", err)
	}
	c := sessionCookieFromRec(rec)
	if c == nil {
		t.Fatal("no session cookie set")
	}
	if !c.HttpOnly {
		t.Error("cookie not HttpOnly")
	}
	if !c.Secure {
		t.Error("cookie not Secure (secure=true)")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.MaxAge <= 0 {
		t.Errorf("MaxAge = %d, want > 0", c.MaxAge)
	}
	// The cookie value is a valid session for host-7.
	hid, err := ring.Verify(c.Value)
	if err != nil || hid != "host-7" {
		t.Fatalf("issued cookie does not verify: hid=%q err=%v", hid, err)
	}
}

func TestSetSession_InsecureWhenNotSecure(t *testing.T) {
	ring := newRing(t, testCurrent)
	a := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, false)
	rec := httptest.NewRecorder()
	_ = a.SetSession(rec, "h")
	if c := sessionCookieFromRec(rec); c == nil || c.Secure {
		t.Fatalf("expected non-Secure cookie when secure=false, got %+v", c)
	}
}

func TestClearSession_ExpiresCookie(t *testing.T) {
	ring := newRing(t, testCurrent)
	a := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, true)
	rec := httptest.NewRecorder()
	a.ClearSession(rec)
	c := sessionCookieFromRec(rec)
	if c == nil || c.MaxAge >= 0 {
		t.Fatalf("expected expired cookie (MaxAge < 0), got %+v", c)
	}
}
