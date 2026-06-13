package auth

import (
	"context"
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
