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

// getDisplayMediaDeferredStubJS stubs getDisplayMedia to return a promise that resolves only when the
// test calls window.__gpResolveShare() — so the test can hold the picker "open" while the host pulls
// eligibility, then resolve it and assert the late-resolving capture is released, not leaked. The
// resolved stream is parked on window.__gpShareStream so the test can read its track readyState.
const getDisplayMediaDeferredStubJS = `
(() => {
  navigator.mediaDevices.getDisplayMedia = () => new Promise((resolve) => {
    window.__gpResolveShare = () => {
      const c = document.createElement('canvas');
      c.width = 64; c.height = 64;
      c.getContext('2d').fillRect(0, 0, 64, 64);
      const s = c.captureStream(5);
      window.__gpShareStream = s;
      resolve(s);
    };
  });
})();
`

// T-13 / AC-13 (edge): the host force-no-shares the guest WHILE its screen picker is still open. When
// the picker later resolves, the capture must be released (not stored or announced) — otherwise the
// getDisplayMedia tracks leak behind a now-locked share control. Proves the post-picker re-validation
// (canStartShare) stops the late stream: its video track ends rather than staying live.
func TestScreenShare_RevokeDuringPickerReleasesCapture(t *testing.T) {
	s := seedDeviceCheck(t)
	if err := s.store.SetPassCanScreen(context.Background(), s.passID, true); err != nil {
		t.Fatalf("grant can_screen: %v", err)
	}

	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	aCtx, cancelA := chromedp.NewContext(alloc)
	defer cancelA()
	aCtx, cancelAT := context.WithTimeout(aCtx, 150*time.Second)
	defer cancelAT()
	injectStub := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(getDisplayMediaDeferredStubJS).Do(ctx)
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
		chromedp.WaitVisible(`.gs-screen[data-screen-state="idle"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest A enter: %v", err)
	}

	// Open the picker (it stays pending until the test resolves it) and confirm it actually opened.
	if err := chromedp.Run(aCtx,
		chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.Poll(`typeof window.__gpResolveShare === 'function'`, nil, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("screen picker did not open: %v", err)
	}

	// Host force-no-shares A while the picker is still open → A gets the share suppression lock.
	hostConn := dialHostWS(t, s)
	defer hostConn.Close(websocket.StatusNormalClosure, "done")
	writeFrame(t, hostConn, signaling.Frame{T: "force-no-share", PeerID: s.passID})
	if err := chromedp.Run(aCtx, chromedp.WaitVisible(`.gs-lock[data-locked="1"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("force-no-share lock notice did not appear: %v", err)
	}

	// Now resolve the picker. The late capture must be released (track ended) — not stored, not
	// announced — and the guest must never enter the backstage state.
	if err := chromedp.Run(aCtx,
		chromedp.Evaluate(`window.__gpResolveShare()`, nil),
		chromedp.Poll(`!!window.__gpShareStream && window.__gpShareStream.getVideoTracks()[0].readyState === 'ended'`,
			nil, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("the capture that resolved after the revoke was not released: %v", err)
	}
	var state string
	if err := chromedp.Run(aCtx, chromedp.AttributeValue(`.gs-screen`, "data-screen-state", &state, nil, chromedp.ByQuery)); err != nil {
		t.Fatalf("read screen state: %v", err)
	}
	if state != "idle" {
		t.Fatalf("screen state after revoked-mid-picker share = %q, want idle (never backstage)", state)
	}
}

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
