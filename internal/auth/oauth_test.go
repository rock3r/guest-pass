package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/rock3r/guest-pass/internal/config"
	"github.com/rock3r/guest-pass/internal/store"
)

// fakeUpserter implements HostUpserter in memory.
type fakeUpserter struct {
	bySub   map[string]*store.Host
	created []store.CreateHostParams
}

func (f *fakeUpserter) GetHostByGoogleSub(_ context.Context, sub string) (*store.Host, error) {
	if h, ok := f.bySub[sub]; ok {
		return h, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeUpserter) CreateHost(_ context.Context, p store.CreateHostParams) (*store.Host, error) {
	f.created = append(f.created, p)
	h := &store.Host{ID: "new-" + p.GoogleSub, GoogleSub: p.GoogleSub, Email: p.Email, Name: p.Name, Picture: p.Picture, IsAdmin: p.IsAdmin, Status: p.Status}
	if f.bySub == nil {
		f.bySub = map[string]*store.Host{}
	}
	f.bySub[p.GoogleSub] = h
	return h, nil
}

// raceUpserter simulates a concurrent first login: the first GetHostByGoogleSub misses,
// CreateHost fails (unique violation, because another request created the row first),
// and a subsequent GetHostByGoogleSub finds that row.
type raceUpserter struct {
	getCalls  int
	host      *store.Host
	createErr error
}

func (f *raceUpserter) GetHostByGoogleSub(_ context.Context, _ string) (*store.Host, error) {
	f.getCalls++
	if f.getCalls == 1 {
		return nil, store.ErrNotFound
	}
	return f.host, nil
}

func (f *raceUpserter) CreateHost(_ context.Context, _ store.CreateHostParams) (*store.Host, error) {
	return nil, f.createErr
}

type fakeFetcher struct {
	info *userInfo
	err  error
}

func (f *fakeFetcher) fetch(context.Context, *oauth2.Token) (*userInfo, error) {
	return f.info, f.err
}

func newGoogle(t *testing.T, up *fakeUpserter, policy LoginPolicy) *GoogleOAuth {
	t.Helper()
	ring := newRing(t, testCurrent)
	authn := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, true)
	return NewGoogleOAuth(GoogleConfig{
		ClientID: "cid", ClientSecret: "secret", BaseURL: "https://gp.example", Policy: policy, Secure: true,
	}, authn, up)
}

func TestGoogleOAuth_StartLogin_SetsStateAndRedirects(t *testing.T) {
	g := newGoogle(t, &fakeUpserter{}, LoginPolicy{SignupMode: config.SignupModeOpen})
	rec := httptest.NewRecorder()
	g.StartLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/google", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if !strings.Contains(loc.Host, "google.com") {
		t.Fatalf("redirect host = %q, want google", loc.Host)
	}
	redirectState := loc.Query().Get("state")
	var stateCookieVal string
	for _, c := range rec.Result().Cookies() {
		if c.Name == stateCookie {
			stateCookieVal = c.Value
			if !c.HttpOnly {
				t.Error("state cookie not HttpOnly")
			}
		}
	}
	if redirectState == "" || redirectState != stateCookieVal {
		t.Fatalf("state mismatch: redirect=%q cookie=%q", redirectState, stateCookieVal)
	}
}

func TestGoogleOAuth_Callback_InvalidState(t *testing.T) {
	g := newGoogle(t, &fakeUpserter{}, LoginPolicy{SignupMode: config.SignupModeOpen})
	// Cookie present but query state differs.
	req := httptest.NewRequest(http.MethodGet, "/auth/google/callback?state=abc&code=xyz", nil)
	req.AddCookie(&http.Cookie{Name: stateCookie, Value: "different"})
	rec := httptest.NewRecorder()
	g.Callback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("state mismatch: code = %d, want 400", rec.Code)
	}
}

func TestGoogleOAuth_CompleteLogin_NewHostOpenMode(t *testing.T) {
	up := &fakeUpserter{}
	g := newGoogle(t, up, LoginPolicy{SignupMode: config.SignupModeOpen})
	g.fetch = &fakeFetcher{info: &userInfo{Sub: "sub-1", Email: "a@example.com", EmailVerified: true, Name: "Aya"}}

	rec := httptest.NewRecorder()
	g.completeLogin(rec, httptest.NewRequest(http.MethodGet, "/", nil), &oauth2.Token{AccessToken: "x"})

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302 to success", rec.Code)
	}
	if len(up.created) != 1 || up.created[0].Status != store.HostActive {
		t.Fatalf("expected one active host created, got %+v", up.created)
	}
	if sessionCookieFromRec(rec) == nil {
		t.Fatal("no session cookie set after login")
	}
}

func TestGoogleOAuth_CompleteLogin_ExistingHost(t *testing.T) {
	up := &fakeUpserter{bySub: map[string]*store.Host{"sub-1": {ID: "h1", GoogleSub: "sub-1", Status: store.HostActive}}}
	g := newGoogle(t, up, LoginPolicy{SignupMode: config.SignupModeOpen})
	g.fetch = &fakeFetcher{info: &userInfo{Sub: "sub-1", Email: "a@example.com", EmailVerified: true}}

	rec := httptest.NewRecorder()
	g.completeLogin(rec, httptest.NewRequest(http.MethodGet, "/", nil), &oauth2.Token{AccessToken: "x"})

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	if len(up.created) != 0 {
		t.Fatalf("existing host should not be re-created, got %+v", up.created)
	}
}

func TestGoogleOAuth_CompleteLogin_AllowlistMissRejected(t *testing.T) {
	up := &fakeUpserter{}
	g := newGoogle(t, up, LoginPolicy{SignupMode: config.SignupModeAllowlist, AllowedHosts: []string{"allowed@example.com"}})
	g.fetch = &fakeFetcher{info: &userInfo{Sub: "sub-x", Email: "stranger@example.com", EmailVerified: true}}

	rec := httptest.NewRecorder()
	g.completeLogin(rec, httptest.NewRequest(http.MethodGet, "/", nil), &oauth2.Token{AccessToken: "x"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("allowlist miss: code = %d, want 403", rec.Code)
	}
	// Crucially: no persistent host row, so adding the email later just works on re-login.
	if len(up.created) != 0 || sessionCookieFromRec(rec) != nil {
		t.Fatal("allowlist miss must not create a host or set a session")
	}
}

func TestResolveHost_ConcurrentFirstLogin(t *testing.T) {
	existing := &store.Host{ID: "h1", GoogleSub: "sub-1", Status: store.HostActive}
	fake := &raceUpserter{host: existing, createErr: errors.New("UNIQUE constraint failed: hosts.google_sub")}
	ring := newRing(t, testCurrent)
	authn := NewAuthenticator(ring, &fakeHosts{byID: map[string]*store.Host{}}, true)
	g := NewGoogleOAuth(GoogleConfig{ClientID: "c", ClientSecret: "s", BaseURL: "https://x", Policy: LoginPolicy{SignupMode: config.SignupModeOpen}}, authn, fake)

	host, err := g.resolveHost(context.Background(), &userInfo{Sub: "sub-1", Email: "a@example.com", EmailVerified: true})
	if err != nil {
		t.Fatalf("concurrent first login should resolve to the existing host, got %v", err)
	}
	if host == nil || host.ID != "h1" {
		t.Fatalf("resolved host = %+v, want existing h1", host)
	}
}

func TestGoogleOAuth_CompleteLogin_UnverifiedRejected(t *testing.T) {
	up := &fakeUpserter{}
	g := newGoogle(t, up, LoginPolicy{SignupMode: config.SignupModeOpen})
	g.fetch = &fakeFetcher{info: &userInfo{Sub: "sub-1", Email: "a@example.com", EmailVerified: false}}

	rec := httptest.NewRecorder()
	g.completeLogin(rec, httptest.NewRequest(http.MethodGet, "/", nil), &oauth2.Token{AccessToken: "x"})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("unverified: code = %d, want 403", rec.Code)
	}
	if len(up.created) != 0 || sessionCookieFromRec(rec) != nil {
		t.Fatal("unverified login must not create a host or set a session")
	}
}

// An ACTIVE host lands on the configured success URL (the host-app dashboard, /app).
func TestGoogleOAuth_CompleteLogin_ActiveHostLandsOnSuccessURL(t *testing.T) {
	up := &fakeUpserter{bySub: map[string]*store.Host{
		"sub-a": {ID: "ha", GoogleSub: "sub-a", Status: store.HostActive},
	}}
	g := newGoogle(t, up, LoginPolicy{SignupMode: config.SignupModeOpen})
	g.successURL = "/app"
	g.fetch = &fakeFetcher{info: &userInfo{Sub: "sub-a", Email: "a@example.com", EmailVerified: true}}

	rec := httptest.NewRecorder()
	g.completeLogin(rec, httptest.NewRequest(http.MethodGet, "/", nil), &oauth2.Token{AccessToken: "x"})

	if loc := rec.Header().Get("Location"); loc != "/app" {
		t.Fatalf("active host post-login Location = %q, want /app", loc)
	}
}

// A PENDING or SUSPENDED host has a valid session but is gated out of the host-app
// dashboard (RequireHost → 403, EN-6). Post-login must send them to the public landing
// ("/"), not dead-end on a bare forbidden page (Cursor Bugbot, M4 PR-1).
func TestGoogleOAuth_CompleteLogin_NonActiveHostLandsOnLanding(t *testing.T) {
	for _, status := range []string{store.HostPending, store.HostSuspended} {
		up := &fakeUpserter{bySub: map[string]*store.Host{
			"sub-n": {ID: "hn", GoogleSub: "sub-n", Status: status},
		}}
		g := newGoogle(t, up, LoginPolicy{SignupMode: config.SignupModeOpen})
		g.successURL = "/app"
		g.fetch = &fakeFetcher{info: &userInfo{Sub: "sub-n", Email: "n@example.com", EmailVerified: true}}

		rec := httptest.NewRecorder()
		g.completeLogin(rec, httptest.NewRequest(http.MethodGet, "/", nil), &oauth2.Token{AccessToken: "x"})

		if rec.Code != http.StatusFound {
			t.Fatalf("status %q: code = %d, want 302", status, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Fatalf("status %q: post-login Location = %q, want / (not the gated dashboard)", status, loc)
		}
		// A session is still issued — they are a real host, just not active yet.
		if sessionCookieFromRec(rec) == nil {
			t.Fatalf("status %q: expected a session cookie even for a non-active host", status)
		}
	}
}
