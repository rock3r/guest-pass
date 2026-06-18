//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// T-12 / AC-13 (transient): a dropped signaling socket shows the reconnecting overlay and retries.
// The guest enters, then its live socket is force-closed; the island surfaces the reconnecting
// state and auto-recovers (a fresh WS rejoins — EN-16 already dropped the closed connection), with
// NO manual intervention and without routing to a terminal error screen.
func TestReconnect_GuestOverlayAndRecovery(t *testing.T) {
	s := seedDeviceCheck(t)

	alloc, cancelA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelA()
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 150*time.Second)
	defer cancelT()

	injectRecorder := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(wsRecorderJS).Do(ctx)
		return err
	})
	if err := chromedp.Run(ctx,
		injectRecorder,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest enter: %v", err)
	}

	// Force-close the live signaling socket → reconnecting overlay, then auto-recovery to live.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__gpCloseLastWS()`, nil)); err != nil {
		t.Fatalf("force-close socket: %v", err)
	}
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`[data-entered="1"][data-pub="reconnecting"] .gs-reconnecting`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a dropped socket did not show the reconnecting overlay: %v", err)
	}
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the guest did not auto-reconnect after a dropped socket: %v", err)
	}
}

// T-12 / AC-13 (terminal): a terminal {t:terminate} routes to the matching error screen and does
// NOT reconnect. The host kicks the guest over an in-process host signaling client (the host-UI
// stand-in); the server emits {t:terminate,reason:kicked} then evicts and revokes the pass
// (refuse-rejoin, RF-22), so the guest must show the terminal "kicked" screen and stay there.
func TestTerminate_GuestKickedRoutesToErrorScreen(t *testing.T) {
	s := seedDeviceCheck(t)
	ctx := enterGuestSession(t, s.base, s.rawToken, "A")

	// Grab the live self-view camera track before the kick so we can prove it is released when the
	// terminal screen takes over (the session won't reconnect, so nothing re-publishes the stream).
	var trackState string
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`(() => { window.__camTrack = document.querySelector('.gs-selfview').srcObject.getVideoTracks()[0]; return window.__camTrack.readyState; })()`,
		&trackState,
	)); err != nil {
		t.Fatalf("capture self-view track: %v", err)
	}
	if trackState != "live" {
		t.Fatalf("self-view camera track = %q before the kick, want live", trackState)
	}

	hostWS := dialHostWS(t, s)
	defer hostWS.Close(websocket.StatusNormalClosure, "")
	writeFrame(t, hostWS, signaling.Frame{T: "kick", PeerID: s.passID})

	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`[data-terminal="kicked"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a kicked guest did not route to the terminal error screen: %v", err)
	}
	// AC-6 (D-37 §8): the terminal "session over" screen carries the "after" 24h-deletion notice.
	var purgeNotice string
	if err := chromedp.Run(ctx, chromedp.Text(`[data-privacy="purge"]`, &purgeNotice, chromedp.ByQuery)); err != nil {
		t.Fatalf("terminal screen missing the 24h-deletion 'after' notice (AC-6): %v", err)
	}
	if !strings.Contains(purgeNotice, "deleted within 24 hours") {
		t.Fatalf("terminal privacy notice = %q, want the 24h-deletion notice", purgeNotice)
	}
	// A terminal terminate must NOT reconnect: the in-session view is gone and stays gone.
	if err := chromedp.Run(ctx, chromedp.WaitNotPresent(`[data-entered]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("a kicked guest must not reconnect: %v", err)
	}
	// …and it must release the camera/mic behind the terminal screen (no device light left on).
	if err := chromedp.Run(ctx, chromedp.Poll(
		`window.__camTrack && window.__camTrack.readyState === "ended"`,
		nil, chromedp.WithPollingTimeout(10*time.Second),
	)); err != nil {
		t.Fatalf("a terminal screen must release the camera: %v", err)
	}
}

// T-12 / AC-13 + RF-22: when reconnection can't succeed because the pass was revoked/expired while
// the socket was down, the /ws upgrade is rejected with a plain HTTP 403 — no {t:terminate} frame —
// so the client must NOT retry forever. After a capped number of failed reconnects the guest routes
// to the terminal "unreachable" screen. Here the pass is revoked in the store (no terminate frame),
// then the live socket is force-closed; every reconnect attempt fails the upgrade until the cap.
func TestReconnect_ExhaustionRoutesToTerminal(t *testing.T) {
	s := seedDeviceCheck(t)

	alloc, cancelA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelA()
	cdpCtx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	cdpCtx, cancelT := context.WithTimeout(cdpCtx, 150*time.Second)
	defer cancelT()

	injectRecorder := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(wsRecorderJS).Do(ctx)
		return err
	})
	if err := chromedp.Run(cdpCtx,
		injectRecorder,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest enter: %v", err)
	}

	// Revoke the pass server-side (NO terminate frame), then drop the live socket. Every reconnect
	// now fails the /ws upgrade with a 403 → after the cap, route to the terminal "unreachable" screen.
	if err := s.store.SetPassStatus(context.Background(), s.passID, store.PassRevoked); err != nil {
		t.Fatalf("revoke pass: %v", err)
	}
	if err := chromedp.Run(cdpCtx, chromedp.Evaluate(`window.__gpCloseLastWS()`, nil)); err != nil {
		t.Fatalf("force-close socket: %v", err)
	}
	if err := chromedp.Run(cdpCtx,
		chromedp.WaitVisible(`[data-pub="reconnecting"]`, chromedp.ByQuery),     // it tries first…
		chromedp.WaitVisible(`[data-terminal="unreachable"]`, chromedp.ByQuery), // …then gives up (RF-22)
	); err != nil {
		t.Fatalf("exhausted reconnects did not route to the terminal screen (RF-22): %v", err)
	}
}
