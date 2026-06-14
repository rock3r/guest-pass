//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
)

// RF-8 receiver-side enforcement (docs/ARCHITECTURE.md:1002-1007, docs/TESTING.md:166). Server
// reject-self-state + cooperating source-side stop are NOT enough in a P2P mesh: a modified guest
// can keep SENDING media. So every cooperating CONSUMER must detach the locked peer's REMOTE track
// from rendering AND OBS output, independent of the target. These tests prove that with a
// NON-COOPERATING publisher and assert on the consumer's rendered track .enabled flag (a disabled
// remote video track renders black but does not reliably zero videoWidth, so .enabled is the robust,
// direct observable — and it is the same MediaStreamTrack object the receiver exposes).

// nonCooperatingPublisherJS makes a publisher IGNORE its own suppression locks: it wraps
// window.WebSocket so inbound {t:"roster"} frames have the locks stripped from the publisher's OWN
// self entry before the island sees them. The island therefore never calls
// publisher.setModalityEnabled(false) and keeps SENDING the "suppressed" track. Other peers' rosters
// and the OBS {t:occupant-locks} frame are untouched, so this isolates RECEIVER-side enforcement from
// the (bypassed) source-side stop. Injected via AddScriptToEvaluateOnNewDocument before any page
// script runs, on the PUBLISHER's browser only.
const nonCooperatingPublisherJS = `
(() => {
  const Native = window.WebSocket;
  const Wrapped = function (url, protocols) {
    const ws = protocols === undefined ? new Native(url) : new Native(url, protocols);
    let handler = null;
    // Forward every native message to the page's onmessage handler, rewriting only the roster so the
    // publisher never learns of a lock on ITSELF (so it never stops its own outbound track).
    ws.addEventListener("message", (e) => {
      if (!handler) return;
      let data = e.data;
      try {
        const f = JSON.parse(e.data);
        if (f && f.t === "roster" && Array.isArray(f.peers)) {
          for (const p of f.peers) {
            if (p && (p.self || p.id === f.self)) p.locks = [];
          }
          data = JSON.stringify(f);
        }
      } catch (_) { /* non-JSON / parse error: pass through untouched */ }
      handler({ data: data });
    });
    // room.js assigns ws.onmessage = fn; capture it instead of binding it natively, so our listener
    // above is the sole dispatch path (the native onmessage stays unset).
    Object.defineProperty(ws, "onmessage", {
      get() { return handler; },
      set(fn) { handler = fn; },
      configurable: true,
    });
    return ws;
  };
  Wrapped.prototype = Native.prototype;
  Wrapped.CONNECTING = Native.CONNECTING;
  Wrapped.OPEN = Native.OPEN;
  Wrapped.CLOSING = Native.CLOSING;
  Wrapped.CLOSED = Native.CLOSED;
  window.WebSocket = Wrapped;
})();
`

// injectScript returns an action that installs js on the target before any page script runs.
func injectScript(js string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(js).Do(ctx)
		return err
	})
}

// trackEnabledIs is a JS expression: the first track of kind ("audio"|"video") in sel's <video>
// srcObject exists and its .enabled === enabled. This is how a receiver-side detach is observed — the
// rendered stream's track IS the receiver's track, so disabling it via getReceivers() flips this flag.
func trackEnabledIs(sel, kind string, enabled bool) string {
	getter := "getVideoTracks"
	if kind == "audio" {
		getter = "getAudioTracks"
	}
	return fmt.Sprintf(
		`(() => { const v = document.querySelector(%q); const s = v && v.srcObject; const t = s && s.%s()[0]; return !!t && t.enabled === %t; })()`,
		sel, getter, enabled,
	)
}

// trackLive is a JS expression: sel's <video> srcObject carries a live track of kind (presence check).
func trackLive(sel, kind string) string {
	getter := "getVideoTracks"
	if kind == "audio" {
		getter = "getAudioTracks"
	}
	return fmt.Sprintf(
		`(() => { const v = document.querySelector(%q); const s = v && v.srcObject; const t = s && s.%s()[0]; return !!t && t.readyState === 'live'; })()`,
		sel, getter,
	)
}

// dataLockedExcludes asserts the publisher did NOT react at source to a modality (its data-locked, set
// from its own roster locks, omits the kind) — i.e. the non-cooperating injection worked, so a
// consumer detach genuinely proves RECEIVER-side enforcement and not the (bypassed) source-side stop.
func dataLockedExcludes(t *testing.T, ctx context.Context, kind string) {
	t.Helper()
	var locked string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('[data-entered]').dataset.locked || ''`, &locked)); err != nil {
		t.Fatalf("read publisher data-locked: %v", err)
	}
	if strings.Contains(locked, kind) {
		t.Fatalf("test invalid: the publisher cooperated (data-locked=%q includes %q) — a consumer detach would not prove receiver-side enforcement", locked, kind)
	}
}

// RF-8 / host greenroom monitor: a NON-cooperating guest keeps sending audio after a force-mute, but
// the host MUST detach the guest's remote AUDIO track from its monitor tile (receiver-side), and
// re-attach it on release. Exercises PeerLink.setRemoteTrackEnabled + greenroom.js applyLocks.
func TestRF8_HostGreenroomDetachesNonCooperatingPublisher(t *testing.T) {
	s := seedGrid(t, 1)
	raw := s.rawTokens[0]

	// Guest: NON-cooperating publisher in its own fake-media browser (kept alive so the host consumes it).
	gAlloc, cancelGA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelGA()
	gCtx, cancelG := chromedp.NewContext(gAlloc)
	defer cancelG()
	gCtx, cancelGT := context.WithTimeout(gCtx, 150*time.Second)
	defer cancelGT()
	if err := chromedp.Run(gCtx,
		injectScript(nonCooperatingPublisherJS),
		chromedp.Navigate(s.base+"/p/"+raw),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("non-cooperating guest publish: %v", err)
	}

	// Host greenroom renders the guest tile over P2P, carrying the guest's live audio track.
	hAlloc, cancelHA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelHA()
	hCtx, cancelH := chromedp.NewContext(hAlloc)
	defer cancelH()
	hCtx, cancelHT := context.WithTimeout(hCtx, 150*time.Second)
	defer cancelHT()
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	const tile = `.gr-tile[data-role="guest"]`
	if err := chromedp.Run(hCtx,
		network.Enable(), setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(tile+` .gr-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('`+tile+` .gr-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(90*time.Second)),
		chromedp.Poll(trackLive(tile+` .gr-video`, "audio"), nil, chromedp.WithPollingTimeout(30*time.Second)),
	); err != nil {
		t.Fatalf("host grid did not render the guest with a live audio track: %v", err)
	}

	// Host force-mutes → the lock shows on the tile, and RF-8 detaches the guest's remote audio track
	// even though the guest keeps sending.
	if err := chromedp.Run(hCtx,
		chromedp.Click(tile+` .gr-force[data-kind="mic"]`, chromedp.ByQuery),
		chromedp.WaitVisible(tile+` .gr-lock`, chromedp.ByQuery),
		chromedp.Poll(trackEnabledIs(tile+` .gr-video`, "audio", false), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("host did not detach the locked guest's remote audio track (RF-8): %v", err)
	}

	// Confirm the publisher genuinely never stopped at source (the detach above is receiver-side only).
	dataLockedExcludes(t, gCtx, "mic")

	// Release → the host re-attaches the remote audio track.
	if err := chromedp.Run(hCtx,
		chromedp.Click(tile+` .gr-release[data-kind="mic"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(tile+` .gr-lock`, chromedp.ByQuery),
		chromedp.Poll(trackEnabledIs(tile+` .gr-video`, "audio", true), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("release did not re-attach the remote audio track: %v", err)
	}
}

// RF-8 / OBS source page (air-critical, exercises the NEW {t:occupant-locks} server frame). A
// NON-cooperating guest keeps sending video; the OBS cam source MUST detach the occupant's remote
// VIDEO track on a force-no-cam and re-attach on release. Phase B proves the lock-before-media path:
// after a reconnect the server re-projects occupant-locks on attach and obs.js re-asserts on the
// fresh track, so the source comes back with the cam still detached (never flashing the locked video).
func TestRF8_OBSSourceDetachesNonCooperatingPublisher(t *testing.T) {
	s := seedDeviceCheck(t)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()

	// Tab 1 = guest publisher (non-cooperating); tab 2 = OBS source, SAME browser for loopback P2P.
	guestCtx, cancelGuest := chromedp.NewContext(allocCtx)
	defer cancelGuest()
	guestCtx, cancelGuestT := context.WithTimeout(guestCtx, 180*time.Second)
	defer cancelGuestT()
	obsCtx, cancelOBS := chromedp.NewContext(guestCtx)
	defer cancelOBS()

	if err := chromedp.Run(guestCtx,
		injectScript(nonCooperatingPublisherJS),
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("non-cooperating guest publish: %v", err)
	}

	// OBS source page: install the WS recorder (for the phase-B reconnect), open the chromeless source.
	if err := chromedp.Run(obsCtx,
		injectScript(wsRecorderJS),
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source page did not load: %v", err)
	}

	// Host binds cam-1 → the guest; the source connects and renders the camera over P2P.
	hostConn := dialHostWS(t, s)
	defer hostConn.Close(websocket.StatusNormalClosure, "done")
	writeFrame(t, hostConn, signaling.Frame{T: "rebind", Slot: s.slotLabel, OccupantPeerID: s.passID})
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("obs source did not render the bound occupant over P2P: %v", err)
	}

	// Phase A — force-no-cam → the OBS source detaches the occupant's remote VIDEO track (RF-8), even
	// though the publisher keeps sending; release re-attaches it.
	writeFrame(t, hostConn, signaling.Frame{T: "force-no-cam", PeerID: s.passID})
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(trackEnabledIs("#obs-video", "video", false), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("OBS source did not detach the locked occupant's video track (RF-8): %v", err)
	}
	dataLockedExcludes(t, guestCtx, "cam")
	writeFrame(t, hostConn, signaling.Frame{T: "release", PeerID: s.passID, Kind: "cam"})
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(trackEnabledIs("#obs-video", "video", true), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("release did not re-attach the OBS source video track: %v", err)
	}

	// Phase B — lock BEFORE media: force again, then drop the OBS socket. On reconnect the server
	// re-projects occupant-locks on attach and obs.js re-asserts on the fresh track, so the source
	// comes back with the cam DETACHED (video disabled) while the audio (unlocked) is live.
	writeFrame(t, hostConn, signaling.Frame{T: "force-no-cam", PeerID: s.passID})
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(trackEnabledIs("#obs-video", "video", false), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("re-force did not detach the OBS video track: %v", err)
	}
	if err := chromedp.Run(obsCtx,
		chromedp.Evaluate(`window.__gpCloseLastWS()`, nil),
		chromedp.Poll(
			`(() => { const v=document.querySelector('#obs-video'); const s=v&&v.srcObject; const vt=s&&s.getVideoTracks()[0]; const at=s&&s.getAudioTracks()[0]; return !!vt && vt.enabled===false && !!at && at.enabled===true; })()`,
			nil, chromedp.WithPollingTimeout(60*time.Second),
		),
	); err != nil {
		t.Fatalf("OBS source did not re-assert the lock on the post-reconnect track (lock-before-media, RF-8): %v", err)
	}
}

// RF-8 / backstage mesh thumbnail: a NON-cooperating guest A keeps sending audio after a force-mute,
// but a peer guest B MUST detach A's remote audio track on B's thumbnail of A (receiver-side over the
// guest↔guest mesh), and re-attach on release. Exercises MeshPeer.setRemoteTrackEnabled +
// MeshManager.setLocks + devicecheck.js wiring — the only coverage of the mesh consumer surface.
func TestRF8_MeshThumbnailDetachesNonCooperatingPublisher(t *testing.T) {
	s := seedDeviceCheck(t)

	// Guest A — NON-cooperating publisher (its own fake-media browser, with the lock-stripping inject).
	aAlloc, cancelAA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAA()
	aCtx, cancelA := chromedp.NewContext(aAlloc)
	defer cancelA()
	aCtx, cancelAT := context.WithTimeout(aCtx, 150*time.Second)
	defer cancelAT()
	if err := chromedp.Run(aCtx,
		injectScript(nonCooperatingPublisherJS),
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"] .gs-selfview`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("non-cooperating guest A enter: %v", err)
	}

	// Guest B — a cooperating CONSUMER that meshes with A and renders A as a backstage thumbnail.
	bCtx := enterGuestSession(t, s.base, s.rawTokenB, "B")

	// B renders A's thumbnail over the mesh, carrying A's live audio track.
	tileA := `.gr-tile[data-guest="` + s.passID + `"] .gr-video`
	if err := chromedp.Run(bCtx,
		chromedp.Poll(`(() => { const v = document.querySelector(`+fmt.Sprintf("%q", tileA)+`); return !!v && v.videoWidth > 0; })()`, nil, chromedp.WithPollingTimeout(90*time.Second)),
		chromedp.Poll(trackLive(tileA, "audio"), nil, chromedp.WithPollingTimeout(30*time.Second)),
	); err != nil {
		t.Fatalf("guest B did not render guest A's backstage thumbnail with audio over the mesh: %v", err)
	}

	// Host force-mutes A → B detaches A's remote audio track on its thumbnail (RF-8), even though A
	// keeps sending; release re-attaches it.
	host := dialHostWS(t, s)
	defer host.Close(websocket.StatusNormalClosure, "")
	writeFrame(t, host, signaling.Frame{T: "force-mute", PeerID: s.passID})
	if err := chromedp.Run(bCtx,
		chromedp.Poll(trackEnabledIs(tileA, "audio", false), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("guest B did not detach the locked peer's remote audio track on the mesh thumbnail (RF-8): %v", err)
	}
	dataLockedExcludes(t, aCtx, "mic")
	writeFrame(t, host, signaling.Frame{T: "release", PeerID: s.passID, Kind: "mic"})
	if err := chromedp.Run(bCtx,
		chromedp.Poll(trackEnabledIs(tileA, "audio", true), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("release did not re-attach the remote audio track on the mesh thumbnail: %v", err)
	}
}
