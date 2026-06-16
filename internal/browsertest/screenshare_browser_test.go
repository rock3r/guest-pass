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

// getDisplayMediaStubJS stubs navigator.mediaDevices.getDisplayMedia (which the fake-device flags do
// NOT cover — they only stub getUserMedia) so the sharer-capture path is exercisable headlessly with
// no real picker + no user gesture. It returns a video-only canvas capture stream (D-41), matching
// the production getDisplayMedia({video:true}) shape. Injected before any page script runs.
const getDisplayMediaStubJS = `
(() => {
  navigator.mediaDevices.getDisplayMedia = async () => {
    const c = document.createElement('canvas');
    c.width = 64; c.height = 64;
    c.getContext('2d').fillRect(0, 0, 64, 64);
    return c.captureStream(5);
  };
})();
`

// T-13 / AC-13: the sharer's screenshare SELF-state. A screenshare-eligible guest starts capture
// (the stubbed getDisplayMedia), which announces {t:screen-start} → the server folds "backstage" into
// the sharer's OWN roster entry (the canonical screen-roster is host-only, EN-8). The host alone
// promotes it to live via {t:screen-select}; only then does the sharer's self-pointer read "live" —
// it never asserts live optimistically. Stopping returns it to idle. This covers the self-state
// derivation end to end; the live MEDIA render (host rail + /s/screen) lands in PR-13/PR-14.
func TestScreenShare_SharerSelfState(t *testing.T) {
	s := seedDeviceCheck(t)

	// Make guest A screenshare-eligible BEFORE it joins (the server seeds can_screen from the pass on
	// join, EN-23) so the share affordance is present without driving the host greenroom toggle.
	if err := s.store.SetPassCanScreen(context.Background(), s.passID, true); err != nil {
		t.Fatalf("grant can_screen: %v", err)
	}

	// Guest A in its own fake-media browser, with the getDisplayMedia stub installed before any page
	// script runs (mirrors enterGuestSession but adds the stub injection).
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	aCtx, cancelA := chromedp.NewContext(alloc)
	defer cancelA()
	aCtx, cancelAT := context.WithTimeout(aCtx, 150*time.Second)
	defer cancelAT()
	injectStub := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(getDisplayMediaStubJS).Do(ctx)
		return err
	})
	if err := chromedp.Run(aCtx,
		injectStub,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"] .gs-selfview`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest A enter: %v", err)
	}

	// IDLE: eligible, capturing nothing → the affordance is present and shows the idle self-state with
	// a "Share screen" button.
	var btn string
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`.gs-screen[data-screen-state="idle"]`, chromedp.ByQuery),
		chromedp.Text(`.gs-screen-toggle`, &btn, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("idle screenshare affordance did not render: %v", err)
	}
	if btn != "Share screen" {
		t.Fatalf("idle button = %q, want %q", btn, "Share screen")
	}

	// START: click share → getDisplayMedia (stub) resolves → {t:screen-start} → the server folds
	// "backstage" into A's own roster entry. A is NOT live (the host hasn't selected it).
	if err := chromedp.Run(aCtx,
		chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="backstage"]`, chromedp.ByQuery),
		chromedp.Text(`.gs-screen-toggle`, &btn, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("share did not transition to active-backstage: %v", err)
	}
	if btn != "Stop sharing" {
		t.Fatalf("sharing button = %q, want %q", btn, "Stop sharing")
	}

	// SELECT-LIVE: the host promotes A to the live screen slot ({t:screen-select}, host-only). Only now
	// does A's OWN self-pointer read "live" — derived from the server fold, never asserted locally.
	hostConn := dialHostWS(t, s)
	defer hostConn.Close(websocket.StatusNormalClosure, "done")
	writeFrame(t, hostConn, signaling.Frame{T: "screen-select", PeerID: s.passID})
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`.gs-screen[data-screen-state="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("host select-live did not fold 'live' into the sharer's self-state: %v", err)
	}

	// STOP: click stop → {t:screen-stop} → the server drops A from the pool AND vacates the live slot
	// (no auto-advance) → A's self-pointer clears back to idle.
	if err := chromedp.Run(aCtx,
		chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="idle"]`, chromedp.ByQuery),
		chromedp.Text(`.gs-screen-toggle`, &btn, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("stop did not return the sharer to idle: %v", err)
	}
	if btn != "Share screen" {
		t.Fatalf("post-stop button = %q, want %q", btn, "Share screen")
	}
}
