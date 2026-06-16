//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
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
// swaps it → taking it off air clears the live render. The /s/screen OBS source has its own test below.
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
	// Both sharers are still backstage, so the host's rail keeps rendering both previews (the host
	// consumer survives the publisher's prune-to-host of non-host viewers on going off air).
	if err := chromedp.Run(hCtx,
		renders(railA+` .gr-screen-video`),
		renders(railB+` .gr-screen-video`),
	); err != nil {
		t.Fatalf("host rail did not keep rendering backstage previews after take-off-air: %v", err)
	}
}

// T-12 / AC-12: the /s/screen OBS source page renders the LIVE sharer's SCREEN over the screen
// channel (D-21) — not its camera. A sharer goes live; the chromeless /s/screen source (authenticated
// by the screenshare slot's source token, EN-15) binds the live occupant and consumes its screen
// publisher. Taking the share off air unbinds the source (the live render clears).
func TestScreenShareMedia_ScreenSourceRendersLiveShare(t *testing.T) {
	s := seedDeviceCheck(t)
	if err := s.store.SetPassCanScreen(context.Background(), s.passID, true); err != nil {
		t.Fatalf("grant can_screen: %v", err)
	}

	// Guest A enters and shares (its screen publisher is now up); A is backstage (not yet live).
	aCtx := enterEligibleSharer(t, s.base, s.rawToken, "A")
	shareScreen(t, aCtx, "A")

	// The /s/screen OBS source page in its own fake-media browser (loopback P2P). It starts unbound
	// (no live share yet) and renders nothing.
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	srcCtx, cancelSrc := chromedp.NewContext(alloc)
	defer cancelSrc()
	srcCtx, cancelSrcT := context.WithTimeout(srcCtx, 180*time.Second)
	defer cancelSrcT()
	if err := chromedp.Run(srcCtx,
		chromedp.Navigate(s.base+"/s/screen?token="+s.srcTokenScreen),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("/s/screen source page did not load: %v", err)
	}

	// Host puts A's share live (screen slot ← A). The source gets the slot-rebind and consumes A's
	// SCREEN publisher over the screen channel, rendering live frames.
	hostConn := dialHostWS(t, s)
	defer hostConn.Close(websocket.StatusNormalClosure, "done")
	writeFrame(t, hostConn, signaling.Frame{T: "screen-select", PeerID: s.passID})
	if err := chromedp.Run(srcCtx, renders(`#obs-video`)); err != nil {
		t.Fatalf("/s/screen did not render the live sharer's screen over P2P: %v", err)
	}

	// A CAMERA lock on the live sharer must NOT black out the /s/screen source — the screen link's
	// only consumed track is the share, so cam/mic locks don't apply to it (D-21). The screen slot's
	// source still receives the occupant-locks projection (RF-8), so wait for the lock to apply, then
	// assert the screen video track stays enabled (force-no-share, which would suppress it, instead
	// pulls the sharer from the live slot, so it never reaches the source as a lock).
	writeFrame(t, hostConn, signaling.Frame{T: "force-no-cam", PeerID: s.passID})
	if err := chromedp.Run(srcCtx,
		chromedp.WaitVisible(`html[data-obs-locks~="cam"]`, chromedp.ByQuery),
		chromedp.Poll(`(() => { const v = document.querySelector('#obs-video'); const s = v && v.srcObject; const t = s && s.getVideoTracks()[0]; return !!t && t.enabled === true; })()`,
			nil, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("a camera lock wrongly disabled the /s/screen share track: %v", err)
	}

	// Take the share off air → the screen slot unbinds → the source clears its surface.
	writeFrame(t, hostConn, signaling.Frame{T: "screen-select", PeerID: ""})
	if err := chromedp.Run(srcCtx, chromedp.Poll(
		`(() => { const v = document.querySelector('#obs-video'); return !!v && !v.srcObject; })()`,
		nil, chromedp.WithPollingTimeout(20*time.Second))); err != nil {
		t.Fatalf("/s/screen did not clear after take-off-air: %v", err)
	}
}
