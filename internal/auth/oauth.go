package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/rock3r/guest-pass/internal/store"
)

const (
	stateCookie = "gp_oauth_state"
	stateTTL    = 10 * time.Minute
	userInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
)

// ErrLoginNotAllowed means the onboarding policy refuses a first-time sign-in (e.g. a
// non-allowlisted email under SIGNUP_MODE=allowlist); no host row is created.
var ErrLoginNotAllowed = errors.New("auth: login not allowed by signup policy")

// googleEndpoint is Google's OAuth2 endpoint, defined inline to avoid pulling in
// golang.org/x/oauth2/google (which drags cloud.google.com/go/compute/metadata in just
// for these two URLs). Values match golang.org/x/oauth2/google.Endpoint.
var googleEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// HostUpserter is the store slice the Google flow needs to resolve a host at login.
// *store.Store satisfies it.
type HostUpserter interface {
	GetHostByGoogleSub(ctx context.Context, sub string) (*store.Host, error)
	CreateHost(ctx context.Context, p store.CreateHostParams) (*store.Host, error)
}

// userInfo is the subset of Google's OpenID userinfo response we consume.
type userInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

// userInfoFetcher fetches the signed-in user's profile given an access token. The real
// implementation calls Google; tests inject a fake.
type userInfoFetcher interface {
	fetch(ctx context.Context, tok *oauth2.Token) (*userInfo, error)
}

// GoogleOAuth implements Google sign-in: redirect → callback → host upsert → session.
type GoogleOAuth struct {
	cfg        *oauth2.Config
	auth       *Authenticator
	hosts      HostUpserter
	policy     LoginPolicy
	fetch      userInfoFetcher
	secure     bool
	successURL string
}

// GoogleConfig bundles the inputs for NewGoogleOAuth.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	Policy       LoginPolicy
	Secure       bool
	SuccessURL   string // where to send the host after a successful login (default "/")
}

// NewGoogleOAuth wires the Google OAuth flow. The redirect URI is
// BASE_URL/auth/google/callback (must match the registered Google client, §12.1).
func NewGoogleOAuth(c GoogleConfig, auth *Authenticator, hosts HostUpserter) *GoogleOAuth {
	cfg := &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  strings.TrimRight(c.BaseURL, "/") + "/auth/google/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     googleEndpoint,
	}
	success := c.SuccessURL
	if success == "" {
		success = "/"
	}
	g := &GoogleOAuth{cfg: cfg, auth: auth, hosts: hosts, policy: c.Policy, secure: c.Secure, successURL: success}
	g.fetch = &googleUserInfoFetcher{cfg: cfg}
	return g
}

// StartLogin handles GET /auth/google: it sets a single-use state cookie (CSRF) and
// redirects to Google's consent screen.
func (g *GoogleOAuth) StartLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomState()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, g.stateCookieValue(state, int(stateTTL/time.Second)))
	http.Redirect(w, r, g.cfg.AuthCodeURL(state), http.StatusFound)
}

// Callback handles GET /auth/google/callback: it validates the state (constant-time),
// exchanges the code, and completes the login.
func (g *GoogleOAuth) Callback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(stateCookie)
	state := r.URL.Query().Get("state")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, g.stateCookieValue("", -1)) // single-use: clear it
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	tok, err := g.cfg.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "oauth exchange failed", http.StatusBadGateway)
		return
	}
	g.completeLogin(w, r, tok)
}

// completeLogin fetches the profile, resolves/creates the host, and sets the session.
// Split from Callback so the post-exchange flow is testable without a real token endpoint.
func (g *GoogleOAuth) completeLogin(w http.ResponseWriter, r *http.Request, tok *oauth2.Token) {
	info, err := g.fetch.fetch(r.Context(), tok)
	if err != nil {
		http.Error(w, "fetching profile failed", http.StatusBadGateway)
		return
	}
	if info.Sub == "" || !info.EmailVerified { // Google email-verified required (D-36)
		http.Error(w, "google account not verified", http.StatusForbidden)
		return
	}
	host, err := g.resolveHost(r.Context(), info)
	if errors.Is(err, ErrLoginNotAllowed) {
		http.Error(w, "this account is not authorized for this instance", http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}
	if err := g.auth.SetSession(w, host.ID); err != nil {
		http.Error(w, "session failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, g.successURL, http.StatusFound)
}

// resolveHost returns the existing host for this Google identity, or creates one per
// the onboarding policy. It returns ErrLoginNotAllowed when the policy refuses a
// first-time sign-in (e.g. a non-allowlisted email under SIGNUP_MODE=allowlist), in
// which case no host row is persisted.
func (g *GoogleOAuth) resolveHost(ctx context.Context, info *userInfo) (*store.Host, error) {
	h, err := g.hosts.GetHostByGoogleSub(ctx, info.Sub)
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("looking up host: %w", err)
	}
	d := g.policy.decideNewHost(info.Email)
	if !d.allowed {
		return nil, ErrLoginNotAllowed
	}
	var picture *string
	if info.Picture != "" {
		picture = &info.Picture
	}
	host, err := g.hosts.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: info.Sub,
		Email:     info.Email,
		Name:      info.Name,
		Picture:   picture,
		IsAdmin:   d.isAdmin,
		Status:    d.status,
	})
	if err != nil {
		// A concurrent first login for the same Google account may have created the row
		// between our lookup and insert (unique google_sub). Re-fetch and use it; if it
		// still isn't there, the failure was real.
		if existing, gerr := g.hosts.GetHostByGoogleSub(ctx, info.Sub); gerr == nil {
			return existing, nil
		}
		return nil, fmt.Errorf("creating host: %w", err)
	}
	return host, nil
}

func (g *GoogleOAuth) stateCookieValue(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     stateCookie,
		Value:    value,
		Path:     "/auth/google",
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}

func randomState() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// googleUserInfoFetcher calls Google's OpenID userinfo endpoint with the access token.
type googleUserInfoFetcher struct{ cfg *oauth2.Config }

func (f *googleUserInfoFetcher) fetch(ctx context.Context, tok *oauth2.Token) (*userInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building userinfo request: %w", err)
	}
	resp, err := f.cfg.Client(ctx, tok).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching userinfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	var info userInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decoding userinfo: %w", err)
	}
	return &info, nil
}
