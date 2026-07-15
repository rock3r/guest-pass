//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
	"github.com/rock3r/guest-pass/internal/web"
)

// gridSeed is a host + N guest passes + a host session cookie, for the multi-guest grid test.
type gridSeed struct {
	base       string
	hostCookie string
	rawTokens  []string
}

// seedGrid creates n guest passes under one host and returns their raw tokens + a host cookie.
func seedGrid(t *testing.T, n int) *gridSeed {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "grid.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	host, err := st.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "grid-sub", Email: "host@example.com", Name: "Host", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	stream, err := st.CreateStream(ctx, store.CreateStreamParams{HostID: host.ID, Title: "Grid Stream"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	hasher, err := token.NewHasher("grid-browser-token-secret-bbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}

	raws := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw, err := token.Mint()
		if err != nil {
			t.Fatalf("mint guest %d: %v", i, err)
		}
		if _, err := st.CreatePass(ctx, store.CreatePassParams{
			StreamID: stream.ID, Name: ptr(fmt.Sprintf("Guest %d", i+1)),
			Role: store.RoleGuest, TokenHash: hasher.Hash(raw), Status: store.PassSent,
		}); err != nil {
			t.Fatalf("CreatePass %d: %v", i, err)
		}
		raws = append(raws, raw)
	}

	ring, err := auth.NewKeyRing("grid-browser-session-secret-cccccccccccc")
	if err != nil {
		t.Fatalf("key ring: %v", err)
	}
	handler, err := web.NewRouter(web.RouterConfig{
		SourceURL: "https://github.com/rock3r/guest-pass/tree/test",
		Hub:       signaling.NewHub(nil, nil),
		Auth:      auth.NewAuthenticator(ring, st, false),
		Store:     st,
		Hasher:    hasher,
		Mailer:    mail.NewLogMailer(io.Discard),
		BaseURL:   "https://gp.example",
		StaticDir: BuildDist(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	sess, err := ring.Issue(host.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue host session: %v", err)
	}
	return &gridSeed{base: Serve(t, handler).URL, hostCookie: sess, rawTokens: raws}
}

// publishGuestOwnBrowser opens a FRESH fake-media browser, runs the device check, and enters →
// the guest publishes its camera and its Room goes live. Each fake-media publisher needs its OWN
// chromedp browser (own synthetic camera) — two publishers can't share one fake device (M2
// learning). The browser stays alive (cancel deferred to test end) so it keeps publishing while
// the host consumes. data-pub="live" means the publishing Room joined the signaling room.
func publishGuestOwnBrowser(t *testing.T, base, rawToken, label string) {
	t.Helper()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(allocCtx)
	t.Cleanup(cancel)
	ctx, cancelT := context.WithTimeout(ctx, 150*time.Second)
	t.Cleanup(cancelT)

	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/p/"+rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("publish guest %s: %v", label, err)
	}
}

// T-9 / AC-10: the host greenroom renders a live multi-guest grid — three guests each publish
// from their own fake-media browser, and the host consumes all three over loopback P2P, one tile
// each, role-filtered, with a per-tile three-state on-air pill (D-24). Three publishers ⇒ three
// browsers; the host is a fourth (a consumer).
func TestGreenroom_MultiGuestGrid(t *testing.T) {
	s := seedGrid(t, 3)
	for i, raw := range s.rawTokens {
		publishGuestOwnBrowser(t, s.base, raw, fmt.Sprintf("g%d", i+1))
	}

	hostAlloc, cancelHA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelHA()
	hostCtx, cancelH := chromedp.NewContext(hostAlloc)
	defer cancelH()
	hostCtx, cancelHT := context.WithTimeout(hostCtx, 150*time.Second)
	defer cancelHT()

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		// All three guest tiles render their P2P video (each publisher → its own tile).
		chromedp.Poll(
			`document.querySelectorAll('.gr-video').length === 3 && `+
				`[...document.querySelectorAll('.gr-video')].every((v) => v.videoWidth > 0)`,
			nil, chromedp.WithPollingTimeout(120*time.Second)),
	); err != nil {
		t.Fatalf("greenroom grid did not render 3 guests over P2P: %v", err)
	}

	// Role-filtered: exactly three tiles (the guests; no host/obs tiles), each with a three-state
	// on-air pill (its value reflects the roster `onAir` — status-unavailable with no OBS source).
	var pills int
	if err := chromedp.Run(hostCtx, chromedp.Evaluate(
		`document.querySelectorAll('.gr-tile[data-role="guest"] .gr-pill[data-onair]').length`, &pills,
	)); err != nil {
		t.Fatalf("pill count: %v", err)
	}
	if pills != 3 {
		t.Fatalf("expected 3 role-filtered guest tiles with on-air pills, got %d", pills)
	}

	// The host's operational controls belong in a persistent People rail, not repeated inside every
	// video tile. It identifies every connected guest and opens the first guest's selected detail.
	var peopleCount, selectedControls, healthCount int
	if err := chromedp.Run(hostCtx, chromedp.Evaluate(
		`document.querySelectorAll('.gr-people-list [data-guest]').length`, &peopleCount,
	), chromedp.Evaluate(
		`document.querySelectorAll('.gr-person-detail[data-guest] .gr-slot').length`, &selectedControls,
	), chromedp.Evaluate(
		`document.querySelectorAll('.gr-person-detail[data-guest] .gr-person-health[data-signal]').length`, &healthCount,
	)); err != nil {
		t.Fatalf("read host People rail: %v", err)
	}
	if peopleCount != 3 || selectedControls != 1 || healthCount != 1 {
		t.Fatalf("People rail = %d people / %d selected controls / %d connection state, want 3 / 1 / 1", peopleCount, selectedControls, healthCount)
	}
}

// A host is also a first-class backstage participant: the control room must provide an explicit
// local setup action (rather than silently opening devices), a self preview/mic control once joined,
// the in-memory backstage chat, and a per-participant audio-activity indicator. This is intentionally
// a browser test because the contract spans getUserMedia, the live room UI, and Preact rendering.
func TestGreenroom_HostSetupChatAndAudioActivity(t *testing.T) {
	s := seedGrid(t, 1)
	publishGuestOwnBrowser(t, s.base, s.rawTokens[0], "guest")

	hostAlloc, cancelHA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelHA()
	hostCtx, cancelH := chromedp.NewContext(hostAlloc)
	defer cancelH()
	hostCtx, cancelHT := context.WithTimeout(hostCtx, 150*time.Second)
	defer cancelHT()

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.gr-host-setup`, chromedp.ByQuery),
		chromedp.Click(`.gr-host-setup`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gr-host-preview`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.gr-host-preview').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.WaitVisible(`.gr-host-mic`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gr-host-audio[role="meter"]`, chromedp.ByQuery),
		chromedp.Click(`.gr-host-mic`, chromedp.ByQuery),
		chromedp.Click(`.gr-host-mic`, chromedp.ByQuery),
		chromedp.Poll(trackEnabledIs(`.gr-host-preview`, "audio", true), nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.Click(`.gr-host-mic`, chromedp.ByQuery),
		chromedp.Poll(trackEnabledIs(`.gr-host-preview`, "audio", false), nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.Click(`.gr-host-mic`, chromedp.ByQuery),
		chromedp.Poll(trackEnabledIs(`.gr-host-preview`, "audio", true), nil, chromedp.WithPollingTimeout(15*time.Second)),
		// The host rail is deliberately a tabbed control surface: People keeps
		// participant and quality operations together, while backstage chat gets
		// its own full-height view rather than being squeezed underneath them.
		chromedp.WaitVisible(`[role="tablist"][aria-label="Control room sidebar"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`#gr-people-tab[aria-selected="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`#gr-people-panel`, chromedp.ByQuery),
		chromedp.Click(`#gr-chat-tab`, chromedp.ByQuery),
		chromedp.WaitVisible(`#gr-chat-tab[aria-selected="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gr-chat-form`, chromedp.ByQuery),
		chromedp.SendKeys(`#gr-chat-draft`, "host-chat-roundtrip-8Nf2", chromedp.ByQuery),
		// Switching to People while composing must not discard an unsent backstage message.
		chromedp.Click(`#gr-people-tab`, chromedp.ByQuery),
		chromedp.WaitVisible(`#gr-people-panel`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#gr-chat-panel').hidden`, nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.Click(`#gr-chat-tab`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gr-chat-form`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#gr-chat-draft').value === 'host-chat-roundtrip-8Nf2'`, nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.Click(`.gr-chat-form button`, chromedp.ByQuery),
		chromedp.Poll(`[...document.querySelectorAll('.gr-chat-messages li')].some((m) => m.textContent.includes('host-chat-roundtrip-8Nf2'))`, nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.WaitVisible(`.gr-audio-activity[data-peer]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("host control-room setup affordances: %v", err)
	}
}
