//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// openHostGreenroom opens the host's /greenroom in its own fake-media browser (the host cookie
// authenticates the WS). The host consumes guests over P2P, so it needs the fake-media allocator for
// loopback WebRTC even though it never publishes. Returns the live ctx (cancels deferred to test end).
func openHostGreenroom(t *testing.T, s *devSeed) context.Context {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelT := context.WithTimeout(ctx, 180*time.Second)
	t.Cleanup(cancelT)
	setHostCookie := chromedp.ActionFunc(func(c context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(c)
	})
	if err := chromedp.Run(ctx,
		network.Enable(), setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.greenroom`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("host greenroom did not load: %v", err)
	}
	return ctx
}

// shareScreen clicks an entered eligible sharer's Share control and waits for its backstage self-state.
func shareScreen(t *testing.T, ctx context.Context, who string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="backstage"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("%s did not reach backstage: %v", who, err)
	}
}

// renders polls until a <video> matching sel has decoded frames (videoWidth > 0) over P2P.
func renders(sel string) chromedp.Action {
	return chromedp.Poll(
		`(() => { const v = document.querySelector(`+jsString(sel)+`); return !!v && v.videoWidth > 0; })()`,
		nil, chromedp.WithPollingTimeout(60*time.Second))
}

// T-12 / AC-11: the screenshare preview-switcher, end to end over real P2P. TWO eligible guests (own
// fake-media browsers) share → the host's rail renders BOTH screens → the host selects one live → the
// host badges it AND every backstage guest renders the live share (AC-11 "for everyone") → re-select
// swaps it → taking it off air clears the live render. The /s/screen OBS source is PR-14.
func TestScreenShareMedia_RailSelectLiveEveryone(t *testing.T) {
	s := seedDeviceCheck(t)
	ctx := context.Background()
	if err := s.store.SetPassCanScreen(ctx, s.passID, true); err != nil {
		t.Fatalf("grant A can_screen: %v", err)
	}
	if err := s.store.SetPassCanScreen(ctx, s.passIDB, true); err != nil {
		t.Fatalf("grant B can_screen: %v", err)
	}

	// Two eligible sharers, each in its own fake-media browser, both sharing.
	aCtx := enterEligibleSharer(t, s.base, s.rawToken, "A")
	bCtx := enterEligibleSharer(t, s.base, s.rawTokenB, "B")
	shareScreen(t, aCtx, "A")
	shareScreen(t, bCtx, "B")

	// Host greenroom: the rail shows BOTH sharers, each rendering its screen over P2P.
	hCtx := openHostGreenroom(t, s)
	railA := `.gr-screen-tile[data-sharer="` + s.passID + `"]`
	railB := `.gr-screen-tile[data-sharer="` + s.passIDB + `"]`
	if err := chromedp.Run(hCtx,
		chromedp.WaitVisible(railA, chromedp.ByQuery),
		chromedp.WaitVisible(railB, chromedp.ByQuery),
		renders(railA+` .gr-screen-video`),
		renders(railB+` .gr-screen-video`),
	); err != nil {
		t.Fatalf("host rail did not show both sharers' screens: %v", err)
	}

	// Host selects A live → A's tile is badged live on the host, and guest B renders A's live screen.
	if err := chromedp.Run(hCtx,
		chromedp.Click(railA+` .gr-screen-select`, chromedp.ByQuery),
		chromedp.WaitVisible(railA+`[data-live="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("select-live A did not badge A live on the host: %v", err)
	}
	if err := chromedp.Run(bCtx, renders(`.gs-livescreen[data-sharer="`+s.passID+`"] .gs-livescreen-video`)); err != nil {
		t.Fatalf("backstage guest B did not render A's live screen (AC-11 everyone): %v", err)
	}

	// Re-select B live → the host swaps the badge to B (A no longer live), and guest A renders B's screen.
	if err := chromedp.Run(hCtx,
		chromedp.Click(railB+` .gr-screen-select`, chromedp.ByQuery),
		chromedp.WaitVisible(railB+`[data-live="1"]`, chromedp.ByQuery),
		chromedp.WaitVisible(railA+`[data-live="0"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("re-select did not swap the live share to B: %v", err)
	}
	if err := chromedp.Run(aCtx, renders(`.gs-livescreen[data-sharer="`+s.passIDB+`"] .gs-livescreen-video`)); err != nil {
		t.Fatalf("guest A did not render B's live screen after the swap: %v", err)
	}

	// Take the share off air (host) → no live render anywhere (no auto-advance), and both tiles unlive.
	if err := chromedp.Run(hCtx,
		chromedp.Click(`.gr-screen-off`, chromedp.ByQuery),
		chromedp.WaitVisible(railB+`[data-live="0"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("take-off-air did not clear the live badge: %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.WaitNotPresent(`.gs-livescreen`, chromedp.ByQuery)); err != nil {
		t.Fatalf("guest A's live render did not clear after take-off-air: %v", err)
	}
}
