//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/signaling"
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

	hostWS := dialHostWS(t, s)
	defer hostWS.Close(websocket.StatusNormalClosure, "")
	writeFrame(t, hostWS, signaling.Frame{T: "kick", PeerID: s.passID})

	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`[data-terminal="kicked"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a kicked guest did not route to the terminal error screen: %v", err)
	}
	// A terminal terminate must NOT reconnect: the in-session view is gone and stays gone.
	if err := chromedp.Run(ctx, chromedp.WaitNotPresent(`[data-entered]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("a kicked guest must not reconnect: %v", err)
	}
}
