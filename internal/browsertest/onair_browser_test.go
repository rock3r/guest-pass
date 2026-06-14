//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"testing"
	"time"

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

	// Guest enters the greenroom; the on-air self pill defaults to status-unavailable.
	if err := chromedp.Run(guestCtx,
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
}
