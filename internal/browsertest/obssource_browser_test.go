//go:build browser

package browsertest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
)

// wsRecorderJS wraps window.WebSocket so a test can force-close the page's live signaling
// socket (to exercise the OBS source's auto-reconnect). It records every socket the page
// opens and exposes __gpCloseLastWS() to close the most recent open one. Injected before any
// page script runs via AddScriptToEvaluateOnNewDocument.
const wsRecorderJS = `
(() => {
  const Native = window.WebSocket;
  window.__gpSockets = [];
  const Wrapped = function (url, protocols) {
    const ws = protocols === undefined ? new Native(url) : new Native(url, protocols);
    window.__gpSockets.push(ws);
    return ws;
  };
  Wrapped.prototype = Native.prototype;
  Wrapped.CONNECTING = Native.CONNECTING;
  Wrapped.OPEN = Native.OPEN;
  Wrapped.CLOSING = Native.CLOSING;
  Wrapped.CLOSED = Native.CLOSED;
  window.WebSocket = Wrapped;
  window.__gpCloseLastWS = () => {
    const open = window.__gpSockets.filter((w) => w.readyState === Native.OPEN);
    if (open.length) open[open.length - 1].close();
  };
})();
`

// dialHostWS opens an in-process /ws connection authenticated by the host session cookie.
// It is the M2 stand-in for the host UI (which lands in M3): it lets a test drive the
// host-only slot rebind that binds a slot to an occupant (EN-7).
func dialHostWS(t *testing.T, s *devSeed) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(s.base, "http", "ws", 1) + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cookie := (&http.Cookie{Name: auth.SessionCookie, Value: s.hostCookie}).String()
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {cookie}},
	})
	if err != nil {
		t.Fatalf("host /ws dial: %v", err)
	}
	return c
}

func writeFrame(t *testing.T, c *websocket.Conn, f signaling.Frame) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c, f); err != nil {
		t.Fatalf("write frame %q: %v", f.T, err)
	}
}

// T-8 / AC-7: the chromeless OBS cam source page renders the slot's bound occupant over P2P.
// A guest publishes (tab 1); a host rebinds cam-1 to that guest over an in-process signaling
// client; the OBS source page (tab 2) at /s/cam-1?token=<srcToken> consumes and renders the
// guest's camera. The source token never appears in the DOM (EN-15), and a dropped signaling
// socket auto-reconnects and re-renders the occupant with no intervention.
func TestOBSSource_RendersBoundOccupant(t *testing.T) {
	s := seedDeviceCheck(t)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()

	// Tab 1 = guest publisher; tab 2 = OBS source, in the SAME browser so loopback P2P works.
	guestCtx, cancelGuest := chromedp.NewContext(allocCtx)
	defer cancelGuest()
	guestCtx, cancelGuestT := context.WithTimeout(guestCtx, 180*time.Second)
	defer cancelGuestT()
	obsCtx, cancelOBS := chromedp.NewContext(guestCtx)
	defer cancelOBS()

	// Guest: open the magic link, run the device check, enter → starts publishing.
	if err := chromedp.Run(guestCtx,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest publish flow: %v", err)
	}

	// OBS source page: install the WS recorder, then open the chromeless source. cam-1 is
	// still unbound, so it shows no media yet.
	injectRecorder := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(wsRecorderJS).Do(ctx)
		return err
	})
	if err := chromedp.Run(obsCtx,
		injectRecorder,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source page did not load: %v", err)
	}

	// Host rebinds cam-1 to the guest (occupant = the guest's pass id). The source — already
	// subscribed to cam-1 — gets a slot-rebind and connects to the guest over P2P.
	hostConn := dialHostWS(t, s)
	defer hostConn.Close(websocket.StatusNormalClosure, "done")
	writeFrame(t, hostConn, signaling.Frame{T: "rebind", Slot: s.slotLabel, OccupantPeerID: s.passID})

	// The OBS source renders live (fake-device) frames from the bound occupant.
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(`!!document.querySelector('#obs-video') && document.querySelector('#obs-video').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("obs source did not render the bound occupant over P2P: %v", err)
	}

	// D-41: the guest's mic rides the cam source into OBS — the page must carry the guest's
	// live audio and must NOT be muted (a muted <video> would silence the program audio OBS
	// captures). Asserted explicitly so a regression to muted/audio-less is caught.
	var audioOK bool
	if err := chromedp.Run(obsCtx, chromedp.Evaluate(`(() => {
		const v = document.querySelector('#obs-video');
		if (!v || v.muted) return false;
		const s = v.srcObject;
		if (!s) return false;
		const a = s.getAudioTracks();
		return a.length > 0 && a[0].readyState === 'live';
	})()`, &audioOK)); err != nil {
		t.Fatalf("read audio track state: %v", err)
	}
	if !audioOK {
		t.Fatalf("OBS cam source must carry the guest's live, unmuted audio (D-41)")
	}

	// EN-15: the source token authenticates the WS, it is never written into the DOM.
	var html string
	if err := chromedp.Run(obsCtx, chromedp.Evaluate(`document.documentElement.outerHTML`, &html)); err != nil {
		t.Fatalf("read DOM: %v", err)
	}
	if strings.Contains(html, s.srcToken) {
		t.Fatalf("source token leaked into the OBS page DOM (EN-15)")
	}

	// Auto-reconnect: force the signaling socket closed. The page tears the link down (video
	// drops to 0), reconnects, re-resolves the still-bound slot, and re-renders the occupant.
	if err := chromedp.Run(obsCtx,
		chromedp.Evaluate(`window.__gpCloseLastWS()`, nil),
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth === 0`,
			nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("obs source did not auto-reconnect and re-render after a dropped socket: %v", err)
	}
}
