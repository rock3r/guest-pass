//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// D-38 network-blocked guest error (docs/ARCHITECTURE.md §8, docs/DEPLOYMENT.md §2). GuestPass media
// is P2P and v1 is STUN-only: a guest behind symmetric NAT / a UDP-blocking firewall can't establish
// ANY direct connection, and must get a clear "your network blocks peer-to-peer" screen rather than a
// silent hang behind a false "you're live". These tests prove the client-side watchdog: with every
// pc forced relay-only and NO TURN configured (so nothing ever connects), the guest shows the
// network-blocked screen; and a normal guest that DOES connect over loopback never shows it (the
// "ever connected" guard — no false positive).

// relayOnlyRTCJS wraps window.RTCPeerConnection so every connection the guest creates is forced to
// iceTransportPolicy:"relay" with no TURN server configured → there is never a usable candidate, so
// no pc (publish or mesh) ever reaches "connected". This faithfully simulates a blocked network
// (symmetric NAT / UDP-blocking firewall) without a production test hook. Injected via
// AddScriptToEvaluateOnNewDocument before any page script runs, on the blocked guest's browser only.
// Mirrors the WebSocket-wrapper pattern in rf8_browser_test.go (the returned native instance keeps
// the real prototype, so createOffer/addTrack/etc. all work).
const relayOnlyRTCJS = `
(() => {
  const Native = window.RTCPeerConnection;
  const Wrapped = function (config) {
    const c = Object.assign({}, config || {});
    c.iceTransportPolicy = "relay"; // relay-only, but no TURN configured → no usable candidate, ever
    return new Native(c);
  };
  Wrapped.prototype = Native.prototype;
  window.RTCPeerConnection = Wrapped;
})();
`

// setNetBlockMsJS shortens the network-blocked watchdog (default 20s in production) so a test doesn't
// wait the full window. It sets the window.__gpNetBlockMs test seam read by ConnectivityWatch — a
// test-only override that never changes the production default (production never sets this global).
func setNetBlockMsJS(ms int) string {
	return fmt.Sprintf("window.__gpNetBlockMs = %d;", ms)
}

// hostGreenroomConsumes opens the host greenroom in its own browser and waits until it is consuming
// the guest (the guest tile is present in the monitor). The greenroom offers to every guest peer, so
// this makes the guest's Publisher create a pc — which is what arms the connectivity watchdog. The
// returned cancel funcs keep the greenroom alive until the caller is done.
func hostGreenroomConsumes(t *testing.T, base, hostCookie string) (context.Context, func()) {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	ctx, cancelT := context.WithTimeout(ctx, 90*time.Second)
	cancel := func() { cancelT(); cancelCtx(); cancelAlloc() }
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, hostCookie).WithURL(base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(ctx,
		network.Enable(), setHostCookie,
		chromedp.Navigate(base+"/greenroom"),
		chromedp.WaitVisible(`.gr-tile[data-role="guest"]`, chromedp.ByQuery),
	); err != nil {
		cancel()
		t.Fatalf("host greenroom did not load / consume the guest: %v", err)
	}
	return ctx, cancel
}

// D-38: a guest whose every P2P connection is forced relay-only with no TURN (a blocked network) must
// show the network-blocked screen, replacing the false "you're live" entered view, rather than hang.
func TestNetBlocked_ForcedRelayOnly_ShowsNetworkBlockedScreen(t *testing.T) {
	s := seedGrid(t, 1)
	raw := s.rawTokens[0]

	// Blocked guest: relay-only RTCPeerConnection (no TURN) + a shortened watchdog (4s).
	gAlloc, cancelGA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelGA()
	gCtx, cancelG := chromedp.NewContext(gAlloc)
	defer cancelG()
	gCtx, cancelGT := context.WithTimeout(gCtx, 90*time.Second)
	defer cancelGT()
	if err := chromedp.Run(gCtx,
		injectScript(relayOnlyRTCJS),
		injectScript(setNetBlockMsJS(4000)),
		chromedp.Navigate(s.base+"/p/"+raw),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery), // signaling is fine; only media is blocked
	); err != nil {
		t.Fatalf("blocked guest enter: %v", err)
	}

	// Host greenroom consumes the guest → its Publisher creates a pc → the watchdog arms.
	_, cancelHost := hostGreenroomConsumes(t, s.base, s.hostCookie)
	defer cancelHost()

	// Within the (shortened) watchdog + margin, the blocked guest shows the network-blocked screen.
	if err := chromedp.Run(gCtx,
		chromedp.WaitVisible(`.dc-netblocked`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("blocked guest did not show the network-blocked screen (D-38): %v", err)
	}

	// The committed copy + the firewall/VPN note expansion (the owner-approved addition).
	var title, note string
	if err := chromedp.Run(gCtx,
		chromedp.Text(`.dc-netblocked-title`, &title, chromedp.ByQuery),
		chromedp.Text(`.dc-netblocked-note`, &note, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("read network-blocked copy: %v", err)
	}
	if !strings.Contains(strings.ToLower(title), "peer-to-peer") {
		t.Fatalf("network-blocked title = %q, want it to mention peer-to-peer", title)
	}
	n := strings.ToLower(note)
	if !strings.Contains(n, "firewall") || !strings.Contains(n, "vpn") || !strings.Contains(n, "hotspot") {
		t.Fatalf("network-blocked note = %q, want it to mention firewall, VPN, and hotspot", note)
	}

	// The Retry control returns to the device-check preview so the guest can switch networks and re-enter.
	if err := chromedp.Run(gCtx,
		chromedp.Click(`.dc-netblocked-retry`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-enter`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Retry did not return the guest to the device-check preview: %v", err)
	}
}

// Control (no false positive): a normal guest whose pc DOES connect over loopback must never show the
// network-blocked screen, even with a watchdog short enough to fire — the "ever connected" guard
// suppresses it. Proves the watchdog won't punish a guest who merely connects slowly.
func TestNetBlocked_NormalGuest_NeverBlocked(t *testing.T) {
	s := seedGrid(t, 1)
	raw := s.rawTokens[0]

	// Normal guest (no relay wrap) with a moderate watchdog (12s) — comfortably above single-guest
	// loopback connect time, so the connection wins and the guard suppresses the timer.
	gAlloc, cancelGA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelGA()
	gCtx, cancelG := chromedp.NewContext(gAlloc)
	defer cancelG()
	gCtx, cancelGT := context.WithTimeout(gCtx, 90*time.Second)
	defer cancelGT()
	if err := chromedp.Run(gCtx,
		injectScript(setNetBlockMsJS(12000)),
		chromedp.Navigate(s.base+"/p/"+raw),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("normal guest enter: %v", err)
	}

	// Host greenroom consumes the guest → its Publisher pc connects over loopback.
	_, cancelHost := hostGreenroomConsumes(t, s.base, s.hostCookie)
	defer cancelHost()

	// The guest's own pc reaches "connected" (everConnected true) — observed via the debug seam.
	if err := chromedp.Run(gCtx,
		chromedp.Poll(`!!window.__gpNetwatch && window.__gpNetwatch.everConnected === true`, nil, chromedp.WithPollingTimeout(45*time.Second)),
	); err != nil {
		t.Fatalf("normal guest never connected over loopback: %v", err)
	}

	// Wait past the watchdog window, then confirm the network-blocked screen never appeared and the
	// guest is still in-session (the "ever connected" guard suppressed the timer).
	if err := chromedp.Run(gCtx,
		chromedp.Sleep(14*time.Second),
	); err != nil {
		t.Fatalf("wait past watchdog: %v", err)
	}
	var blocked, entered, everFlagged bool
	if err := chromedp.Run(gCtx,
		chromedp.Evaluate(`document.querySelector('.dc-netblocked') !== null`, &blocked),
		chromedp.Evaluate(`document.querySelector('[data-entered="1"]') !== null`, &entered),
		// everFlagged catches even a TRANSIENT block screen that onRecovered would have cleared from
		// the DOM before this check — the watchdog must never have fired for a guest that connects.
		chromedp.Evaluate(`!!window.__gpNetwatch && window.__gpNetwatch.flaggedBlocked === true`, &everFlagged),
	); err != nil {
		t.Fatalf("read final state: %v", err)
	}
	if blocked {
		t.Fatalf("a connected guest wrongly showed the network-blocked screen (false positive)")
	}
	if everFlagged {
		t.Fatalf("the watchdog fired for a guest that connected (transient false positive) — the 'ever connected' guard failed")
	}
	if !entered {
		t.Fatalf("a connected guest is no longer in-session (expected to stay entered)")
	}
}
