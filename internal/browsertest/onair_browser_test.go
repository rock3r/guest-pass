//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/signaling"
)

// T-10 / AC-9 — the three-state on-air reflection (D-24). A guest publishes and enters the
// greenroom; its self pill starts at status-unavailable (no OBS signal). The OBS cam source
// page is bound to the guest, then reports OBS program transitions via window.obsstudio
// (obsSourceActiveChanged) — which the server resolves to the slot occupant and forwards —
// flipping the guest's pill on-air → not-on-air. obsStreamingStarted lights the global
// "we're live" indicator. The reducer-level epoch/broadcast invariants are in reducer_test.go.
func TestOnAir_ThreeStateReflection(t *testing.T) {
	s := seedDeviceCheck(t)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()

	// The guest publishes (needs its own fake-media browser); the OBS source consumes. They
	// connect as P2P peers over loopback.
	guestCtx, cancelGuest := chromedp.NewContext(allocCtx)
	defer cancelGuest()
	guestCtx, cancelT := context.WithTimeout(guestCtx, 150*time.Second)
	defer cancelT()
	obsCtx, cancelOBS := chromedp.NewContext(guestCtx)
	defer cancelOBS()

	// The WS recorder (on both the guest and OBS tabs) lets us force-close a tab's signaling
	// socket later to prove the on-air pill degrades when its reflection source disappears.
	injectRecorder := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(wsRecorderJS).Do(ctx)
		return err
	})

	// Guest enters the greenroom; the on-air self pill defaults to status-unavailable.
	if err := chromedp.Run(guestCtx,
		injectRecorder,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-onair[data-onair="status-unavailable"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest enter + default on-air pill: %v", err)
	}

	// OBS source page subscribes to cam-1.
	if err := chromedp.Run(obsCtx,
		injectRecorder,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source load: %v", err)
	}

	// Host binds cam-1 to the guest; once the OBS source renders the guest it holds the slot
	// epoch it will echo on its on-air reports.
	hostConn := dialHostWS(t, s)
	defer hostConn.Close(websocket.StatusNormalClosure, "done")
	writeFrame(t, hostConn, signaling.Frame{T: "rebind", Slot: s.slotLabel, OccupantPeerID: s.passID})
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("obs did not render the bound guest: %v", err)
	}

	// fireObsActive dispatches the OBS program transition the source page listens for.
	fireObsActive := func(active bool) {
		t.Helper()
		expr := fmt.Sprintf(`window.dispatchEvent(new CustomEvent("obsSourceActiveChanged",{detail:{active:%t}}))`, active)
		if err := chromedp.Run(obsCtx, chromedp.Evaluate(expr, nil)); err != nil {
			t.Fatalf("dispatch obsSourceActiveChanged(%t): %v", active, err)
		}
	}

	// Source goes on-program → the guest's pill flips to on-air.
	fireObsActive(true)
	if err := chromedp.Run(guestCtx,
		chromedp.WaitVisible(`.dc-onair[data-onair="on-air"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("on-air pill did not reflect on-air: %v", err)
	}

	// Source leaves the active scene → not-on-air (NOT status-unavailable — we have a signal).
	fireObsActive(false)
	if err := chromedp.Run(guestCtx,
		chromedp.WaitVisible(`.dc-onair[data-onair="not-on-air"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("on-air pill did not reflect not-on-air: %v", err)
	}

	// Broadcast-level "we're live" (obsStreamingStarted) reaches the guest's global indicator.
	if err := chromedp.Run(obsCtx,
		chromedp.Evaluate(`window.dispatchEvent(new CustomEvent("obsStreamingStarted"))`, nil),
	); err != nil {
		t.Fatalf("dispatch obsStreamingStarted: %v", err)
	}
	if err := chromedp.Run(guestCtx,
		chromedp.WaitVisible(`.dc-live[data-live="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("global 'we're live' indicator did not appear: %v", err)
	}

	// Re-arm on-air, then drop the OBS source's signaling socket: with no live OBS reflection
	// the pill must degrade to status-unavailable (D-24: never assert on-air when unknown),
	// not keep showing a stale on-air. (The source auto-reconnects but stays unknown until a
	// fresh transition.)
	fireObsActive(true)
	if err := chromedp.Run(guestCtx,
		chromedp.WaitVisible(`.dc-onair[data-onair="on-air"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("pill did not re-arm to on-air: %v", err)
	}
	if err := chromedp.Run(obsCtx, chromedp.Evaluate(`window.__gpCloseLastWS()`, nil)); err != nil {
		t.Fatalf("force-close obs socket: %v", err)
	}
	if err := chromedp.Run(guestCtx,
		chromedp.WaitVisible(`.dc-onair[data-onair="status-unavailable"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("pill did not degrade to status-unavailable when the OBS source disconnected: %v", err)
	}

	// Client-side degrade (D-24): the GUEST's own signaling socket dropping means it can no
	// longer receive OBS reflections — the on-air pill and the global "we're live" indicator
	// must reset, not keep asserting their last values. Re-arm both (the OBS source reconnected
	// after the previous step), then drop the guest's socket.
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("OBS source did not auto-reconnect before the client-disconnect check: %v", err)
	}
	fireObsActive(true)
	if err := chromedp.Run(guestCtx,
		chromedp.WaitVisible(`.dc-onair[data-onair="on-air"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-live[data-live="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("could not re-arm on-air + live before the client-disconnect check: %v", err)
	}
	if err := chromedp.Run(guestCtx, chromedp.Evaluate(`window.__gpCloseLastWS()`, nil)); err != nil {
		t.Fatalf("force-close guest socket: %v", err)
	}
	if err := chromedp.Run(guestCtx,
		chromedp.WaitVisible(`[data-entered="1"][data-pub="disconnected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-onair[data-onair="status-unavailable"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.dc-live`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a dropped guest socket must reset the reflected on-air + live state (D-24): %v", err)
	}
}

// D-24: a streaming transition that fires while the OBS source socket is reconnecting must not
// be lost. streaming is global and the server doesn't clear it on a source drop, so a dropped
// transition would otherwise strand a stale/absent live banner. The source re-asserts its last
// known streaming state on reconnect.
func TestOnAir_StreamingTransitionSurvivesSourceReconnect(t *testing.T) {
	s := seedDeviceCheck(t)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	guestCtx, cancelGuest := chromedp.NewContext(allocCtx)
	defer cancelGuest()
	guestCtx, cancelT := context.WithTimeout(guestCtx, 150*time.Second)
	defer cancelT()
	obsCtx, cancelOBS := chromedp.NewContext(guestCtx)
	defer cancelOBS()

	// Guest enters — no live banner yet (no streaming reported).
	if err := chromedp.Run(guestCtx,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.dc-live`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest enter: %v", err)
	}

	// OBS source connects (a streaming reflection needs no slot binding — it's global).
	injectRecorder := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(wsRecorderJS).Do(ctx)
		return err
	})
	if err := chromedp.Run(obsCtx,
		injectRecorder,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
		chromedp.Poll(`window.__gpSockets.some((w) => w.readyState === 1)`, nil, chromedp.WithPollingTimeout(30*time.Second)),
	); err != nil {
		t.Fatalf("obs source connect: %v", err)
	}

	// Drop the source socket, then fire streamingStarted DURING the reconnect window — the send
	// is lost, but the page re-asserts the state once it reconnects.
	if err := chromedp.Run(obsCtx,
		chromedp.Evaluate(`window.__gpCloseLastWS()`, nil),
		chromedp.Evaluate(`window.dispatchEvent(new CustomEvent("obsStreamingStarted"))`, nil),
	); err != nil {
		t.Fatalf("drop + fire streamingStarted: %v", err)
	}

	// The guest's live banner appears only because the source re-asserted the dropped transition
	// after reconnecting.
	if err := chromedp.Run(guestCtx,
		chromedp.WaitVisible(`.dc-live[data-live="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a streaming transition dropped during a source reconnect was not re-asserted: %v", err)
	}
}
