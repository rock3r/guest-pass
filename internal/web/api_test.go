package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

const (
	apiTestBaseURL     = "https://gp.example"
	apiTestTokenSecret = "api-test-token-secret-bbbbbbbbbbbbbbbb"
)

// captureMailer records the last invite so tests can assert the magic link without
// going through stdout, and can confirm the raw token round-trips into /p/{token}.
type captureMailer struct {
	mu   sync.Mutex
	last *mail.Invite
	err  error
}

func (m *captureMailer) SendInvite(_ context.Context, inv mail.Invite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := inv
	m.last = &cp
	return nil
}

func (m *captureMailer) lastInvite(t *testing.T) mail.Invite {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last == nil {
		t.Fatal("expected an invite to have been sent")
	}
	return *m.last
}

type apiHarness struct {
	h      http.Handler
	store  *store.Store
	ring   *auth.KeyRing
	hasher *token.Hasher
	mailer *captureMailer
}

func newAPIHarness(t *testing.T) *apiHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ring, err := auth.NewKeyRing("api-router-test-secret-aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("key ring: %v", err)
	}
	authn := auth.NewAuthenticator(ring, st, false)
	hasher, err := token.NewHasher(apiTestTokenSecret)
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	mailer := &captureMailer{}

	h, err := NewRouter(RouterConfig{
		SourceURL:   testSourceURL,
		Auth:        authn,
		Store:       st,
		Hasher:      hasher,
		Mailer:      mailer,
		BaseURL:     apiTestBaseURL,
		RateLimiter: NewRateLimiter(1000, 1000),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return &apiHarness{h: h, store: st, ring: ring, hasher: hasher, mailer: mailer}
}

// host creates an active host and returns it with a valid session cookie.
func (a *apiHarness) host(t *testing.T, sub string) (*store.Host, *http.Cookie) {
	t.Helper()
	h, err := a.store.CreateHost(context.Background(), store.CreateHostParams{
		GoogleSub: sub, Email: sub + "@example.com", Name: sub, Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	tok, err := a.ring.Issue(h.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return h, &http.Cookie{Name: auth.SessionCookie, Value: tok}
}

func (a *apiHarness) req(t *testing.T, method, target, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// createStream is a helper that creates a stream for the given host and returns its id.
func (a *apiHarness) createStream(t *testing.T, cookie *http.Cookie, title string) string {
	t.Helper()
	rec := a.req(t, http.MethodPost, "/api/streams", `{"title":"`+title+`"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create stream = %d, body %s", rec.Code, rec.Body.String())
	}
	var sv struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sv); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	if sv.ID == "" || sv.Title != title {
		t.Fatalf("unexpected stream view: %+v", sv)
	}
	return sv.ID
}

func TestAPI_StreamRoutesRequireAuth(t *testing.T) {
	a := newAPIHarness(t)
	if rec := a.req(t, http.MethodGet, "/api/streams", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/streams without cookie = %d, want 401", rec.Code)
	}
}

func TestAPI_CreateAndListStreams(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "host1")

	id := a.createStream(t, cookie, "My Show")

	rec := a.req(t, http.MethodGet, "/api/streams", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var list []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("list = %+v, want one stream %s", list, id)
	}

	get := a.req(t, http.MethodGet, "/api/streams/"+id, "", cookie)
	if get.Code != http.StatusOK {
		t.Fatalf("get = %d", get.Code)
	}
}

func TestAPI_CreateStreamRequiresTitle(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "host1")
	rec := a.req(t, http.MethodPost, "/api/streams", `{"title":"   "}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank title = %d, want 400", rec.Code)
	}
}

func TestAPI_RejectsUnknownFields(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "host1")
	rec := a.req(t, http.MethodPost, "/api/streams", `{"title":"ok","bogus":1}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400", rec.Code)
	}
}

// A host must not be able to read, delete, or mint passes against another host's stream;
// the API answers 404 so ids can't be probed.
func TestAPI_StreamOwnershipIsolation(t *testing.T) {
	a := newAPIHarness(t)
	_, aliceCookie := a.host(t, "alice")
	_, bobCookie := a.host(t, "bob")

	streamID := a.createStream(t, aliceCookie, "Alice's Show")

	cases := []struct {
		method, target, body string
	}{
		{http.MethodGet, "/api/streams/" + streamID, ""},
		{http.MethodDelete, "/api/streams/" + streamID, ""},
		{http.MethodGet, "/api/streams/" + streamID + "/passes", ""},
		{http.MethodPost, "/api/streams/" + streamID + "/passes", `{"email":"g@example.com"}`},
	}
	for _, c := range cases {
		rec := a.req(t, c.method, c.target, c.body, bobCookie)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as non-owner = %d, want 404", c.method, c.target, rec.Code)
		}
	}

	// Alice can still delete her own stream.
	if rec := a.req(t, http.MethodDelete, "/api/streams/"+streamID, "", aliceCookie); rec.Code != http.StatusNoContent {
		t.Fatalf("owner delete = %d, want 204", rec.Code)
	}
}

func TestAPI_CreatePassSendsMagicLinkWithoutLeakingToken(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "host1")
	streamID := a.createStream(t, cookie, "Launch Stream")

	rec := a.req(t, http.MethodPost, "/api/streams/"+streamID+"/passes",
		`{"email":"guest@example.com","name":"Dana","role":"guest"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pass = %d, body %s", rec.Code, rec.Body.String())
	}

	var pv struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pv); err != nil {
		t.Fatalf("decode pass: %v", err)
	}
	if pv.Status != store.PassSent {
		t.Errorf("pass status = %q, want sent", pv.Status)
	}

	inv := a.mailer.lastInvite(t)
	if !strings.HasPrefix(inv.MagicLink, apiTestBaseURL+"/p/") {
		t.Fatalf("magic link = %q, want prefix %s/p/", inv.MagicLink, apiTestBaseURL)
	}
	raw := strings.TrimPrefix(inv.MagicLink, apiTestBaseURL+"/p/")
	if raw == "" {
		t.Fatal("magic link has no token")
	}

	// The raw token (and thus the magic link) must never appear in the API response: only
	// HMAC(secret, token) is persisted, and the link goes out solely via mail (EN-5).
	if strings.Contains(rec.Body.String(), raw) {
		t.Error("API response leaked the raw magic-link token")
	}

	// What is stored is the hash, and it resolves the pass we just created.
	stored, err := a.store.GetPassByTokenHash(context.Background(), a.hasher.Hash(raw))
	if err != nil {
		t.Fatalf("token hash does not resolve a pass: %v", err)
	}
	if stored.ID != pv.ID {
		t.Fatalf("token resolves pass %s, want %s", stored.ID, pv.ID)
	}
}

func TestAPI_CreatePassRequiresEmail(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "host1")
	streamID := a.createStream(t, cookie, "Stream")
	rec := a.req(t, http.MethodPost, "/api/streams/"+streamID+"/passes", `{"name":"NoEmail"}`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing email = %d, want 400", rec.Code)
	}
}

// mintPass creates a sent pass directly and returns its id and raw magic-link token.
func (a *apiHarness) mintPass(t *testing.T, streamID, name string) (string, string) {
	t.Helper()
	raw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	email := "guest@example.com"
	namePtr := &name
	p, err := a.store.CreatePass(context.Background(), store.CreatePassParams{
		StreamID: streamID, Name: namePtr, Email: &email, Role: store.RoleGuest,
		TokenHash: a.hasher.Hash(raw), Status: store.PassSent,
	})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	return p.ID, raw
}

// EN-10: GET /p/{token} is side-effect-free. It renders the landing page but must not
// transition the pass (e.g. to "opened"), so link prefetchers/scanners can't false-fire.
func TestAPI_PassLandingIsSideEffectFree(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "host1")
	streamID := a.createStream(t, cookie, "Side Effect Stream")
	passID, raw := a.mintPass(t, streamID, "Dana")

	rec := a.req(t, http.MethodGet, "/p/"+raw, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /p/{token} = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Side Effect Stream") {
		t.Error("landing page missing stream title")
	}
	if !strings.Contains(body, "Dana") {
		t.Error("landing page missing guest name")
	}

	reloaded, err := a.store.GetPass(context.Background(), passID)
	if err != nil {
		t.Fatalf("GetPass: %v", err)
	}
	if reloaded.Status != store.PassSent {
		t.Errorf("pass status after GET = %q, want unchanged sent (EN-10)", reloaded.Status)
	}
	if reloaded.OpenedAt != nil {
		t.Error("GET /p/{token} must not stamp opened_at (EN-10)")
	}
}

func TestAPI_PassLandingUnknownToken(t *testing.T) {
	a := newAPIHarness(t)
	rec := a.req(t, http.MethodGet, "/p/this-token-does-not-exist", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token = %d, want 404", rec.Code)
	}
}

func TestAPI_PassLandingRevokedIsGone(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "host1")
	streamID := a.createStream(t, cookie, "Revoked Stream")
	passID, raw := a.mintPass(t, streamID, "Dana")
	if err := a.store.SetPassStatus(context.Background(), passID, store.PassRevoked); err != nil {
		t.Fatalf("SetPassStatus: %v", err)
	}
	rec := a.req(t, http.MethodGet, "/p/"+raw, "", nil)
	if rec.Code != http.StatusGone {
		t.Fatalf("revoked token = %d, want 410", rec.Code)
	}
}

// A minimal config (no Store/Hasher/Mailer) must not expose the host API or /p/ routes —
// the guard keeps the landing-only test/build path intact.
func TestAPI_RoutesAbsentWithoutDeps(t *testing.T) {
	h := testRouter(t, NewRateLimiter(1000, 1000))
	for _, target := range []string{"/api/streams", "/p/sometoken"} {
		if rec := do(h, http.MethodGet, target); rec.Code != http.StatusNotFound {
			t.Errorf("%s with no API deps = %d, want 404", target, rec.Code)
		}
	}
}
