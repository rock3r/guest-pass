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
    c.width = 320; c.height = 180;
    const ctx = c.getContext('2d');
    // Animate so captureStream delivers fresh frames a remote consumer can actually decode
    // (videoWidth > 0) — the live-share render path (AC-11) is asserted end-to-end over P2P.
    let i = 0;
    setInterval(() => {
      i = (i + 9) % 256;
      ctx.fillStyle = "rgb(" + i + ",90,170)";
      ctx.fillRect(0, 0, 320, 180);
    }, 100);
    const s = c.captureStream(10);
    window.__gpShareStream = s; // parked so a test can read the capture track's readyState
    return s;
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

// enterEligibleSharer opens a fake-media browser with the (immediate) getDisplayMedia stub plus any
// extra pre-navigation scripts, runs the device check, and enters the in-session view for an
// already-screenshare-eligible guest. Returns the live ctx.
func enterEligibleSharer(t *testing.T, base, rawToken string, extraScripts ...string) context.Context {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelT := context.WithTimeout(ctx, 150*time.Second)
	t.Cleanup(cancelT)
	inject := chromedp.ActionFunc(func(c context.Context) error {
		for _, js := range append([]string{getDisplayMediaStubJS}, extraScripts...) {
			if _, err := page.AddScriptToEvaluateOnNewDocument(js).Do(c); err != nil {
				return err
			}
		}
		return nil
	})
	if err := chromedp.Run(ctx,
		inject,
		chromedp.Navigate(base+"/p/"+rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"] .gs-selfview`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="idle"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("eligible sharer enter: %v", err)
	}
	return ctx
}

// T-13 / AC-13 (transient reconnect): an active screen share must survive a signaling blip. The
// server runs `leave` on the dropped socket (removing the sharer from the preview pool), so the
// fresh-join roster reads screenShare:"" — but the guest still holds an eligible capture, so the
// client re-asserts {t:screen-start} and recovers into the pool instead of tearing the share down.
// The capture track stays live throughout (parity with the camera republish).
func TestScreenShare_SurvivesReconnect(t *testing.T) {
	s := seedDeviceCheck(t)
	if err := s.store.SetPassCanScreen(context.Background(), s.passID, true); err != nil {
		t.Fatalf("grant can_screen: %v", err)
	}
	aCtx := enterEligibleSharer(t, s.base, s.rawToken, wsRecorderJS)

	// Share → backstage.
	if err := chromedp.Run(aCtx,
		chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="backstage"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("share did not reach backstage: %v", err)
	}

	// Force-close the live signaling socket → reconnecting overlay → auto-recovery to live.
	if err := chromedp.Run(aCtx, chromedp.Evaluate(`window.__gpCloseLastWS()`, nil)); err != nil {
		t.Fatalf("force-close ws: %v", err)
	}
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`[data-entered="1"][data-pub="reconnecting"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest did not reconnect: %v", err)
	}

	// After recovery the share is back in the pool (backstage) and the capture never stopped.
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`.gs-screen[data-screen-state="backstage"]`, chromedp.ByQuery),
		chromedp.Poll(`!!window.__gpShareStream && window.__gpShareStream.getVideoTracks()[0].readyState === 'live'`,
			nil, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("the screen share did not survive the reconnect (re-assert into the pool, capture kept live): %v", err)
	}
}

// T-13 / AC-13 (moderation): the host force-no-shares a guest that is ALREADY sharing in the backstage
// pool. The server pulls it (screenShare:"" + a share suppression lock), so the roster-sync sees a
// genuine host pull (canStartShare false) and stops the capture — the track ends and the self-state
// returns to idle, distinct from the transient-reconnect re-assert above.
func TestScreenShare_ForceNoShareWhileSharingStopsCapture(t *testing.T) {
	s := seedDeviceCheck(t)
	if err := s.store.SetPassCanScreen(context.Background(), s.passID, true); err != nil {
		t.Fatalf("grant can_screen: %v", err)
	}
	aCtx := enterEligibleSharer(t, s.base, s.rawToken)

	if err := chromedp.Run(aCtx,
		chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="backstage"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("share did not reach backstage: %v", err)
	}

	hostConn := dialHostWS(t, s)
	defer hostConn.Close(websocket.StatusNormalClosure, "done")
	writeFrame(t, hostConn, signaling.Frame{T: "force-no-share", PeerID: s.passID})

	// The host pull stops the capture (track ended) and clears the self-state to idle; the
	// force-no-share lock notice shows.
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`.gs-lock[data-locked="1"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="idle"]`, chromedp.ByQuery),
		chromedp.Poll(`!!window.__gpShareStream && window.__gpShareStream.getVideoTracks()[0].readyState === 'ended'`,
			nil, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("force-no-share while sharing did not stop the capture + return to idle: %v", err)
	}
}

// T-13 / AC-13 (revoke while disconnected): a guest is sharing, its socket drops, and the host
// revokes eligibility before it reconnects. The live revoke no-ops (the peer is absent → no share
// lock is created), so the fresh-join roster reads screenShare:"" + canScreen:false with NO lock —
// indistinguishable in a single frame from the transient join roster that precedes the eligibility
// re-seed. The deferred reconcile waits one grace window: no re-seed arrives (genuinely revoked), so
// the stranded capture — which the now-hidden .gs-screen control can't stop — is released.
func TestScreenShare_RevokeWhileDisconnectedStopsCapture(t *testing.T) {
	s := seedDeviceCheck(t)
	if err := s.store.SetPassCanScreen(context.Background(), s.passID, true); err != nil {
		t.Fatalf("grant can_screen: %v", err)
	}
	aCtx := enterEligibleSharer(t, s.base, s.rawToken, wsRecorderJS)

	if err := chromedp.Run(aCtx,
		chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="backstage"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("share did not reach backstage: %v", err)
	}

	// Revoke eligibility in the store (the rejoin handshake re-seeds from passes.can_screen): writing
	// it BEFORE the drop removes any race — the reconnect's join will read can_screen=false. No live
	// revoke action is sent, so no share lock exists (matching the "revoked while absent" scenario).
	if err := s.store.SetPassCanScreen(context.Background(), s.passID, false); err != nil {
		t.Fatalf("revoke can_screen: %v", err)
	}

	// Drop the socket → the guest auto-reconnects and rejoins ineligible.
	if err := chromedp.Run(aCtx, chromedp.Evaluate(`window.__gpCloseLastWS()`, nil)); err != nil {
		t.Fatalf("force-close ws: %v", err)
	}
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest did not reconnect: %v", err)
	}

	// The available share action becomes an explicit unavailable state, and after the reconcile grace
	// the stranded capture is released (track ended) rather than running invisibly forever.
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`.gs-screen[data-eligible="0"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.Poll(`!!window.__gpShareStream && window.__gpShareStream.getVideoTracks()[0].readyState === 'ended'`,
			nil, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("a capture stranded by a revoke-while-disconnected was not released: %v", err)
	}
}

// T-13 / AC-13 (control): while the socket is reconnecting, an active sharer's Stop control must stay
// ENABLED — the capture is intentionally kept alive for recovery, and stopping is a best-effort send
// plus an unconditional local teardown (it does not need a live socket), so the sharer is never stuck
// unable to stop. Distinct from the chat/raise-hand controls, which DO gate on a live socket.
func TestScreenShare_StopStaysEnabledWhileReconnecting(t *testing.T) {
	s := seedDeviceCheck(t)
	if err := s.store.SetPassCanScreen(context.Background(), s.passID, true); err != nil {
		t.Fatalf("grant can_screen: %v", err)
	}
	aCtx := enterEligibleSharer(t, s.base, s.rawToken, wsRecorderJS)

	if err := chromedp.Run(aCtx,
		chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-screen[data-screen-state="backstage"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("share did not reach backstage: %v", err)
	}

	// Drop the socket and catch a reconnecting tick where the Stop control is NOT disabled. The
	// conjunction can only hold if the button is ungated from liveness while sharing (the old
	// disabled={!live} kept it disabled for the whole reconnecting window).
	if err := chromedp.Run(aCtx, chromedp.Evaluate(`window.__gpCloseLastWS()`, nil)); err != nil {
		t.Fatalf("force-close ws: %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.Poll(
		`!!document.querySelector('[data-entered][data-pub="reconnecting"]') && !document.querySelector('.gs-screen-toggle').disabled`,
		nil, chromedp.WithPollingTimeout(20*time.Second))); err != nil {
		t.Fatalf("the Stop control was disabled while reconnecting (a sharer must always be able to stop): %v", err)
	}
}
