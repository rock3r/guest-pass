package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

const wsTestTokenSecret = "ws-test-token-secret-cccccccccccccccc"

// wsHarness is an in-process server wired with credential auth (AD-5 [INT]): a real
// store seeded with hosts/streams/passes/slots, the JWT key ring, and the token hasher.
// It dials /ws as each role over a real WebSocket so the auth→role→room path is
// exercised end to end.
type wsHarness struct {
	srv     *httptest.Server
	store   *store.Store
	ring    *auth.KeyRing
	hasher  *token.Hasher
	hub     *signaling.Hub
	logs    *syncBuffer
	limiter *RateLimiter // WS reconnect limiter; nil unless a test opts in
	ice     []signaling.ICEServer
}

// syncBuffer is a goroutine-safe buffer for capturing slog output across the request
// goroutines httptest spawns.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type wsHarnessOpts struct {
	ice     []signaling.ICEServer
	limiter *RateLimiter
}

func newWSHarness(t *testing.T, o wsHarnessOpts) *wsHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ws.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ring, err := auth.NewKeyRing("ws-router-test-secret-dddddddddddddddd")
	if err != nil {
		t.Fatalf("key ring: %v", err)
	}
	authn := auth.NewAuthenticator(ring, st, false)
	hasher, err := token.NewHasher(wsTestTokenSecret)
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))

	hub := signaling.NewHub()
	var inflight sync.WaitGroup
	h, err := NewRouter(RouterConfig{
		SourceURL:     testSourceURL,
		Hub:           hub,
		Auth:          authn,
		Store:         st,
		Hasher:        hasher,
		Mailer:        mail.NewLogMailer(&bytes.Buffer{}),
		BaseURL:       "https://gp.example",
		ICEServers:    o.ice,
		WSInflight:    &inflight,
		WSRateLimiter: o.limiter,
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &wsHarness{srv: srv, store: st, ring: ring, hasher: hasher, hub: hub, logs: logs, limiter: o.limiter, ice: o.ice}
}

func (h *wsHarness) seedHost(t *testing.T, sub string, status string) (*store.Host, *http.Cookie) {
	t.Helper()
	host, err := h.store.CreateHost(context.Background(), store.CreateHostParams{
		GoogleSub: sub, Email: sub + "@example.com", Name: sub, Status: status,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	tok, err := h.ring.Issue(host.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	return host, &http.Cookie{Name: auth.SessionCookie, Value: tok}
}

func (h *wsHarness) seedStream(t *testing.T, hostID string) *store.Stream {
	t.Helper()
	s, err := h.store.CreateStream(context.Background(), store.CreateStreamParams{HostID: hostID, Title: "show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	return s
}

// seedPass mints a pass and returns the raw magic-link token plus the pass record.
func (h *wsHarness) seedPass(t *testing.T, streamID, role, status string, expiresAt *int64) (string, *store.Pass) {
	t.Helper()
	raw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	p, err := h.store.CreatePass(context.Background(), store.CreatePassParams{
		StreamID: streamID, Role: role, TokenHash: h.hasher.Hash(raw), Status: status, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	return raw, p
}

// seedCamSlot mints a cam slot source token and returns the raw token plus the slot.
func (h *wsHarness) seedCamSlot(t *testing.T, hostID string, idx int64) (string, *store.Slot) {
	t.Helper()
	raw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	sl, err := h.store.CreateSlot(context.Background(), store.CreateSlotParams{
		HostID: hostID, Kind: store.SlotCam, Idx: &idx, SourceTokenHash: h.hasher.Hash(raw),
	})
	if err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	return raw, sl
}

// dial opens a /ws connection with the given query string and optional headers. It
// returns the connection (nil on failure), the handshake response, and any error.
func (h *wsHarness) dial(t *testing.T, qs string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/ws"
	if qs != "" {
		url += "?" + qs
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
}

// dialOK dials and fails the test if the handshake is rejected.
func (h *wsHarness) dialOK(t *testing.T, qs string, header http.Header) *websocket.Conn {
	t.Helper()
	c, _, err := h.dial(t, qs, header)
	if err != nil {
		t.Fatalf("dial %q: handshake failed: %v", qs, err)
	}
	return c
}

func wsReadFrame(t *testing.T, c *websocket.Conn) signaling.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var f signaling.Frame
	if err := wsjsonRead(ctx, c, &f); err != nil {
		t.Fatalf("read: %v", err)
	}
	return f
}

// --- Credential → admission/role ---

func TestWS_RejectsMissingCredential(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	_, resp, err := h.dial(t, "", nil)
	if err == nil {
		t.Fatal("expected handshake to be rejected with no credential")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

func TestWS_HostCookieAdmitted(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{ice: []signaling.ICEServer{{URLs: []string{"stun:stun.example.org:3478"}}}})
	_, cookie := h.seedHost(t, "host1", store.HostActive)
	c := h.dialOK(t, "", cookieHeader(cookie))
	defer c.CloseNow()
	// First frame is the ICE join-ack, proving the host was admitted and joined.
	if f := wsReadFrame(t, c); f.T != "ice" {
		t.Fatalf("first frame = %q, want ice", f.T)
	}
}

func TestWS_GuestPassAdmitted(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	raw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	c := h.dialOK(t, "pass="+raw, nil)
	defer c.CloseNow()
}

func TestWS_RejectsUnknownPass(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	_, resp, err := h.dial(t, "pass=not-a-real-token", nil)
	if err == nil {
		t.Fatal("expected unknown pass to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

func TestWS_RejectsRevokedPass(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	raw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassRevoked, nil)
	_, resp, err := h.dial(t, "pass="+raw, nil)
	if err == nil {
		t.Fatal("expected revoked pass to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestWS_RejectsExpiredPassByStatus(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	raw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassExpired, nil)
	_, resp, err := h.dial(t, "pass="+raw, nil)
	if err == nil {
		t.Fatal("expected expired pass to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestWS_RejectsExpiredPassByDeadline(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	past := time.Now().Add(-time.Hour).Unix()
	raw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, &past)
	_, resp, err := h.dial(t, "pass="+raw, nil)
	if err == nil {
		t.Fatal("expected past-deadline pass to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

// A suspended host's guests are rejected mid-session — the host status is read LIVE at
// the handshake (EN-6), not baked into the pass.
func TestWS_RejectsGuestWhenHostSuspended(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostSuspended)
	stream := h.seedStream(t, host.ID)
	raw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	_, resp, err := h.dial(t, "pass="+raw, nil)
	if err == nil {
		t.Fatal("expected guest of a suspended host to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestWS_RejectsSuspendedHostCookie(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	_, cookie := h.seedHost(t, "host1", store.HostSuspended)
	_, resp, err := h.dial(t, "", cookieHeader(cookie))
	if err == nil {
		t.Fatal("expected suspended host cookie to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

// A pending (not-yet-approved) host is rejected on the /ws path too — status is read
// LIVE (EN-6), so the same non-active gate applies as for a suspended host.
func TestWS_RejectsPendingHostCookie(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	_, cookie := h.seedHost(t, "host1", store.HostPending)
	_, resp, err := h.dial(t, "", cookieHeader(cookie))
	if err == nil {
		t.Fatal("expected pending host cookie to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

func TestWS_SrcTokenAdmittedAndSubscribesSlot(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	raw, slot := h.seedCamSlot(t, host.ID, 1)
	c := h.dialOK(t, "src="+raw, nil)
	defer c.CloseNow()
	// An OBS source subscribes to its slot and is immediately told the current binding.
	if f := wsReadFrame(t, c); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}
	// The src handshake records leak-detection metadata on the slot (AD-23/EN-5).
	got, err := h.store.GetSlot(context.Background(), slot.ID)
	if err != nil {
		t.Fatalf("GetSlot: %v", err)
	}
	if got.SourceTokenLastUsedAt == nil {
		t.Fatal("expected source_token_last_used_at to be stamped on the src handshake")
	}
}

func TestWS_RejectsUnknownSrcToken(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	_, resp, err := h.dial(t, "src=not-a-real-token", nil)
	if err == nil {
		t.Fatal("expected unknown src token to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

// --- Role governs actions (EN-7): only a host rebinds a slot ---

// A host (cookie) rebinds a slot to a guest's pass id and the subscribed OBS source
// receives the slot-rebind. Two authenticated clients, role from auth.
func TestWS_HostRebindReachesSource(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, cookie := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	srcRaw, _ := h.seedCamSlot(t, host.ID, 1)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	src := h.dialOK(t, "src="+srcRaw, nil)
	defer src.CloseNow()
	if f := wsReadFrame(t, src); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}

	guest := h.dialOK(t, "pass="+passRaw, nil)
	defer guest.CloseNow()

	hostConn := h.dialOK(t, "", cookieHeader(cookie))
	defer hostConn.CloseNow()
	wsWriteFrame(t, hostConn, signaling.Frame{T: "rebind", Slot: "cam-1", OccupantPeerID: pass.ID})

	f := wsReadFrame(t, src)
	if f.T != "slot-rebind" || f.OccupantPeerID != pass.ID || f.Epoch != 1 {
		t.Fatalf("source frame = %+v, want slot-rebind(%s, epoch 1)", f, pass.ID)
	}
}

// A guest's rebind is ignored: role is from auth, and slot binding is host-only (EN-7).
func TestWS_GuestRebindIgnored(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	srcRaw, _ := h.seedCamSlot(t, host.ID, 1)
	passRaw, pass := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	src := h.dialOK(t, "src="+srcRaw, nil)
	defer src.CloseNow()
	if f := wsReadFrame(t, src); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}

	guest := h.dialOK(t, "pass="+passRaw, nil)
	defer guest.CloseNow()
	// A guest attempts to hijack the slot; the server must ignore it (host-only).
	wsWriteFrame(t, guest, signaling.Frame{T: "rebind", Slot: "cam-1", OccupantPeerID: pass.ID})

	// The source must NOT receive a rebind. Probe with a short read deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	var f signaling.Frame
	if err := wsjsonRead(ctx, src, &f); err == nil && f.T == "slot-rebind" {
		t.Fatalf("guest rebind was honored: %+v", f)
	}
}

// --- Origin policy ---

// An OBS-CEF source may send a literal "null" Origin; the token-authenticated src path
// must admit it (the slot token is the credential, not an ambient cookie).
func TestWS_NullOriginAllowedForSource(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	srcRaw, _ := h.seedCamSlot(t, host.ID, 1)
	c := h.dialOK(t, "src="+srcRaw, http.Header{"Origin": {"null"}})
	defer c.CloseNow()
	if f := wsReadFrame(t, c); f.T != "slot-unbound" {
		t.Fatalf("source first frame = %q, want slot-unbound", f.T)
	}
}

// The null-Origin relaxation is for source-token connections ONLY (TESTING.md §WS): a
// guest (?pass=) in a normal browser always sends a real Origin, so a literal "null" on
// the guest path is rejected — host/guest Origin validation is not weakened.
func TestWS_NullOriginRejectedForGuest(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	raw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)
	_, resp, err := h.dial(t, "pass="+raw, http.Header{"Origin": {"null"}})
	if err == nil {
		t.Fatal("expected a null Origin on the guest path to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

// A cross-origin handshake on the cookie path is rejected (CSRF defense): the cookie is
// ambient, so a foreign Origin must not be admitted.
func TestWS_CrossOriginRejectedForCookie(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	_, cookie := h.seedHost(t, "host1", store.HostActive)
	hdr := cookieHeader(cookie)
	hdr.Set("Origin", "https://evil.example")
	_, resp, err := h.dial(t, "", hdr)
	if err == nil {
		t.Fatal("expected cross-origin cookie handshake to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

// --- Token redaction (EN-16) ---

func TestWS_TokenRedactedInLogs(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	const secret = "SUPERSECRETPASSTOKEN12345"
	_, _, err := h.dial(t, "pass="+secret, nil)
	if err == nil {
		t.Fatal("expected the bogus pass to be rejected (so a rejection is logged)")
	}
	logs := h.logs.String()
	if logs == "" {
		t.Fatal("expected the rejected handshake to be logged")
	}
	if strings.Contains(logs, secret) {
		t.Fatalf("token leaked into logs: %s", logs)
	}
	if !strings.Contains(logs, "REDACTED") {
		t.Fatalf("expected a REDACTED marker in the logged URL, got: %s", logs)
	}
}

func TestRedactQueryTokens(t *testing.T) {
	cases := map[string]string{
		"/ws?pass=abc123":                "/ws?pass=REDACTED",
		"/ws?src=xyz789":                 "/ws?src=REDACTED",
		"/ws?pass=abc&src=xyz":           "/ws?pass=REDACTED&src=REDACTED",
		"/ws?session=s1&pass=abc&peer=p": "/ws?pass=REDACTED&peer=p&session=s1",
		"/ws":                            "/ws",
	}
	for in, want := range cases {
		if got := redactWSURL(mustParseURL(t, in)); got != want {
			t.Errorf("redactWSURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- One connection per identity (EN-16) ---

func TestWS_OneConnPerIdentityEviction(t *testing.T) {
	h := newWSHarness(t, wsHarnessOpts{})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	raw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	first := h.dialOK(t, "pass="+raw, nil)
	defer first.CloseNow()
	// A second connection with the same pass evicts the first (same peer identity).
	second := h.dialOK(t, "pass="+raw, nil)
	defer second.CloseNow()

	// The first connection receives terminate:reconnect before its socket closes.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var f signaling.Frame
		if err := wsjsonRead(ctx, first, &f); err != nil {
			t.Fatalf("evicted conn closed before a terminate frame: %v", err)
		}
		if f.T == "terminate" {
			if f.Reason != "reconnect" {
				t.Fatalf("terminate reason = %q, want reconnect", f.Reason)
			}
			return
		}
	}
}

// --- Reconnect rate-limit ---

func TestWS_ReconnectRateLimited(t *testing.T) {
	// A tiny bucket: 1 connection, then refused.
	h := newWSHarness(t, wsHarnessOpts{limiter: NewRateLimiter(0.01, 1)})
	host, _ := h.seedHost(t, "host1", store.HostActive)
	stream := h.seedStream(t, host.ID)
	raw, _ := h.seedPass(t, stream.ID, store.RoleGuest, store.PassSent, nil)

	c := h.dialOK(t, "pass="+raw, nil)
	defer c.CloseNow()

	_, resp, err := h.dial(t, "pass="+raw, nil)
	if err == nil {
		t.Fatal("expected the second rapid connection to be rate-limited")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %v, want 429", resp)
	}
}
