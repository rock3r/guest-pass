//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
	"github.com/rock3r/guest-pass/internal/web"
)

func ptr[T any](v T) *T { return &v }

// devSeed is a running real router (with the signaling hub) over a store seeded with one
// active host, a stream, a sent guest pass, and one cam slot, plus the credentials the
// browser tabs need.
type devSeed struct {
	store          *store.Store
	base           string // server base URL
	rawToken       string // guest A's raw magic-link token (/p/{rawToken})
	passID         string // guest A's pass id (== A's signaling peer id)
	rawTokenB      string // guest B's raw magic-link token (a second guest, for live re-route)
	passIDB        string // guest B's pass id (== B's signaling peer id)
	hostCookie     string // host session JWT for the gp_session cookie (/greenroom + host /ws)
	srcToken       string // cam-1 slot's raw source token (/s/{slotLabel}?token=…)
	slotLabel      string // the cam slot's signaling label ("cam-1")
	srcTokenScreen string // the screenshare slot's raw source token (/s/screen?token=…)
	hostID         string // the host's id (room/session key)
	streamID       string // the seeded stream's id (host-app routes)
	slotID         string // the cam-1 slot's DB id (regenerate route)
}

func seedDeviceCheck(t *testing.T) *devSeed {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "devcheck.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	host, err := st.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "dc-sub", Email: "host@example.com", Name: "Host", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	stream, err := st.CreateStream(ctx, store.CreateStreamParams{HostID: host.ID, Title: "Device Check Stream"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	hasher, err := token.NewHasher("devcheck-browser-token-secret-bbbbbbbb")
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	raw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	pass, err := st.CreatePass(ctx, store.CreatePassParams{
		StreamID: stream.ID, Name: ptr("Dana"), Role: store.RoleGuest, TokenHash: hasher.Hash(raw), Status: store.PassSent,
	})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}

	// A second guest (B) so the tracer can re-route a slot live from one occupant to another.
	rawB, err := token.Mint()
	if err != nil {
		t.Fatalf("mint B: %v", err)
	}
	passB, err := st.CreatePass(ctx, store.CreatePassParams{
		StreamID: stream.ID, Name: ptr("Erin"), Role: store.RoleGuest, TokenHash: hasher.Hash(rawB), Status: store.PassSent,
	})
	if err != nil {
		t.Fatalf("CreatePass B: %v", err)
	}

	// A cam-1 slot with its own source token: the OBS source page authenticates the /ws it
	// opens with this token (EN-15), resolving to slot "cam-1" server-side.
	srcRaw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint src: %v", err)
	}
	camSlot, err := st.CreateSlot(ctx, store.CreateSlotParams{
		HostID: host.ID, Kind: store.SlotCam, Idx: ptr(int64(1)), SourceTokenHash: hasher.Hash(srcRaw),
	})
	if err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}

	// The shared screenshare slot with its own source token (signaling label "screen"): the
	// /s/screen OBS source authenticates with this token and consumes the live sharer's SCREEN
	// publisher over the screen channel (D-21/AC-12).
	srcScreenRaw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint screen src: %v", err)
	}
	if _, err := st.CreateSlot(ctx, store.CreateSlotParams{
		HostID: host.ID, Kind: store.SlotScreenshare, SourceTokenHash: hasher.Hash(srcScreenRaw),
	}); err != nil {
		t.Fatalf("CreateSlot screenshare: %v", err)
	}

	ring, err := auth.NewKeyRing("devcheck-browser-session-secret-cccccccc")
	if err != nil {
		t.Fatalf("key ring: %v", err)
	}
	handler, err := web.NewRouter(web.RouterConfig{
		SourceURL: "https://github.com/rock3r/guest-pass/tree/test",
		Hub:       signaling.NewHub(nil, nil),
		Auth:      auth.NewAuthenticator(ring, st, false),
		Store:     st,
		Hasher:    hasher,
		Mailer:    mail.NewLogMailer(io.Discard),
		BaseURL:   "https://gp.example",
		StaticDir: BuildDist(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	sess, err := ring.Issue(host.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue host session: %v", err)
	}
	return &devSeed{
		store: st, base: Serve(t, handler).URL, rawToken: raw, passID: pass.ID,
		rawTokenB: rawB, passIDB: passB.ID,
		hostCookie: sess, srcToken: srcRaw, slotLabel: "cam-1", srcTokenScreen: srcScreenRaw,
		hostID: host.ID, streamID: stream.ID, slotID: camSlot.ID,
	}
}

// T-6/AC-5+AC-6: the device-check renders a live preview, a bare GET never marks opened
// (EN-10), explicit entry transitions to opened, and on entry the camera CONTINUES (it is
// handed to the greenroom publisher, PR-7) rather than being released.
func TestDeviceCheck_PreviewAndExplicitEntry(t *testing.T) {
	s := seedDeviceCheck(t)
	ctx := context.Background()

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("navigate: %v", err)
		}
		if p, _ := s.store.GetPass(ctx, s.passID); p.Status != store.PassSent || p.OpenedAt != nil {
			t.Fatalf("a bare GET must not mark opened (EN-10): status=%q openedAt=%v", p.Status, p.OpenedAt)
		}

		var trackState string
		if err := chromedp.Run(cctx,
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Evaluate(`(() => { window.__dcTrack = document.querySelector('.dc-video').srcObject.getVideoTracks()[0]; return window.__dcTrack.readyState; })()`, &trackState),
		); err != nil {
			t.Fatalf("preview did not render live frames: %v", err)
		}
		if trackState != "live" {
			t.Fatalf("preview camera track = %q, want live", trackState)
		}

		// Explicit entry → opened, and the camera keeps running (handed to the publisher).
		if err := chromedp.Run(cctx,
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("entry did not complete: %v", err)
		}
		if err := chromedp.Run(cctx, chromedp.Evaluate(`window.__dcTrack.readyState`, &trackState)); err != nil {
			t.Fatalf("read track state: %v", err)
		}
		if trackState != "live" {
			t.Fatalf("after entry the camera must keep running for the greenroom, track = %q", trackState)
		}
		p, err := s.store.GetPass(ctx, s.passID)
		if err != nil {
			t.Fatalf("GetPass: %v", err)
		}
		if p.Status != store.PassOpened || p.OpenedAt == nil {
			t.Fatalf("after entry, status=%q openedAt=%v, want opened + stamped", p.Status, p.OpenedAt)
		}
	})
}

// M5.5/AC-2 (DESIGN §6 guest-left): a guest who clicks "Leave the greenroom" gets the voluntary
// guest-left screen — a rejoin path (D-40, rejoin-while-live) and the 24h purge notice (D-37) — NOT
// a terminal error. Rejoin returns to the device-check so they can re-enter.
func TestDeviceCheck_LeaveShowsGuestLeftThenRejoins(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 90*time.Second, func(cctx context.Context) {
		var leftTxt string
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
			// Leave voluntarily → the guest-left screen.
			chromedp.Click(`.gs-leave`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-state="guest-left"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-rejoin`, chromedp.ByQuery),
			chromedp.Text(`[data-state="guest-left"]`, &leftTxt, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("leave → guest-left screen failed: %v", err)
		}
		if !strings.Contains(strings.ToLower(leftTxt), "left the greenroom") {
			t.Fatalf("guest-left screen missing the left copy:\n%s", leftTxt)
		}
		// The GDPR purge reassurance must be present (D-37 / AC-6).
		if !strings.Contains(strings.ToLower(leftTxt), "deleted within 24 hours") {
			t.Fatalf("guest-left screen missing the 24h purge notice:\n%s", leftTxt)
		}

		// Rejoin returns to the device-check preview (re-capture → re-enter). It is gated until the
		// out-of-band leave POST settles (so a rejoin can't race the vacate), so wait for it to enable.
		if err := chromedp.Run(cctx,
			chromedp.WaitEnabled(`.dc-rejoin`, chromedp.ByQuery),
			chromedp.Click(`.dc-rejoin`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("rejoin did not return to the preview: %v", err)
		}
	})
}

// M5.5/AC-2 (DESIGN §6 host-waiting): a guest connects to the greenroom room immediately (it exists
// before the host opens it, D-40), so an early guest is genuinely LIVE but alone. The status reads
// "waiting for the host" — not the bare "you're live" — until a host appears in the roster. The seed
// has an active host account but never opens a host /ws, so no host is present in the room.
func TestDeviceCheck_NoHostYetShowsWaiting(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 90*time.Second, func(cctx context.Context) {
		var txt string
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			// Live in the greenroom, but with no host present → host-waiting (NOT the bare "you're live").
			chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-state="host-waiting"]`, chromedp.ByQuery),
			chromedp.Text(`[data-state="host-waiting"]`, &txt, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("host-waiting did not render: %v", err)
		}
		if !strings.Contains(strings.ToLower(txt), "waiting for the host") {
			t.Fatalf("host-waiting copy missing the waiting message:\n%s", txt)
		}
	})
}

// M5.5/AC-2 (DESIGN §6 cam-blocked): when the guest denies the camera/mic permission, the device
// check shows a distinct "blocked" screen — actionable unblock copy + a Try-again — not a raw error
// name. getUserMedia is overridden to reject with NotAllowedError (the permission-denied signal).
func TestDeviceCheck_PermissionDeniedShowsBlockedScreen(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 90*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			// Make the capture reject as if the guest dismissed/blocked the permission prompt.
			chromedp.Evaluate(`Object.defineProperty(navigator.mediaDevices, 'getUserMedia', {
				configurable: true, writable: true,
				value: () => Promise.reject(new DOMException('denied', 'NotAllowedError')),
			})`, nil),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-error[data-error-kind="blocked"]`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("blocked screen did not render: %v", err)
		}

		var txt string
		var hasRetry bool
		if err := chromedp.Run(cctx,
			chromedp.Text(`.dc-error[data-error-kind="blocked"]`, &txt, chromedp.ByQuery),
			chromedp.Evaluate(`!!document.querySelector('.dc-error[data-error-kind="blocked"] .dc-retry')`, &hasRetry),
		); err != nil {
			t.Fatalf("read blocked screen: %v", err)
		}
		if !strings.Contains(strings.ToLower(txt), "blocked") {
			t.Fatalf("blocked screen copy missing the unblock guidance:\n%s", txt)
		}
		if !hasRetry {
			t.Fatal("blocked screen must offer a Try-again (re-granting permission can recover)")
		}
	})
}

// M5.5/AC-2 (DESIGN §6 unsupported): a browser with no WebRTC (old engine, or an in-app webview
// that strips it) gets a terminal "can't join" screen up front — before any permission prompt — and
// NO Try-again, since a retry can't add the missing API. getUserMedia is removed to simulate it.
func TestDeviceCheck_NoWebRTCShowsUnsupportedScreen(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 90*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			// Strip media capture so the up-front WebRTC-support check fails.
			chromedp.Evaluate(`Object.defineProperty(navigator.mediaDevices, 'getUserMedia', {
				configurable: true, writable: true, value: undefined,
			})`, nil),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-error[data-error-kind="unsupported"]`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("unsupported screen did not render: %v", err)
		}

		var hasRetry bool
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(`!!document.querySelector('.dc-error[data-error-kind="unsupported"] .dc-retry')`, &hasRetry),
		); err != nil {
			t.Fatalf("read unsupported screen: %v", err)
		}
		if hasRetry {
			t.Fatal("unsupported screen must NOT offer a retry — a retry can't add WebRTC")
		}
	})
}

// M5.5 device picker (pre-join UX): the preview phase offers a camera + microphone chooser
// populated from enumerateDevices, the dropdown reflects the device actually captured, picking a
// different device re-acquires getUserMedia with the deviceId constraint, and the chosen device
// persists into the published session stream (no mid-session re-pick needed). Fake media exposes
// one camera but several microphones, so the switch is exercised on the mic.
func TestDeviceCheck_DevicePickerSelectsAndPersists(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		// Run the device check → the preview renders with the camera + mic pickers present.
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-cam-select`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-mic-select`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("preview + device picker did not render: %v", err)
		}

		// The dropdowns are populated from enumerateDevices (real, selectable devices only).
		var camOpts, micOpts int
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(`[...document.querySelectorAll('.dc-cam-select option')].filter((o) => o.value).length`, &camOpts),
			chromedp.Evaluate(`[...document.querySelectorAll('.dc-mic-select option')].filter((o) => o.value).length`, &micOpts),
		); err != nil {
			t.Fatalf("read option counts: %v", err)
		}
		if camOpts < 1 || micOpts < 1 {
			t.Fatalf("device pickers not populated: cams=%d mics=%d (want >=1 each)", camOpts, micOpts)
		}

		// The camera dropdown reflects the device actually being captured in the preview.
		var camSel, activeCam string
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(`document.querySelector('.dc-cam-select').value`, &camSel),
			chromedp.Evaluate(`document.querySelector('.dc-video').srcObject.getVideoTracks()[0].getSettings().deviceId`, &activeCam),
		); err != nil {
			t.Fatalf("read camera selection: %v", err)
		}
		if camSel == "" || camSel != activeCam {
			t.Fatalf("camera picker = %q but captured device = %q; the dropdown must reflect the live camera", camSel, activeCam)
		}

		// Pick a DIFFERENT microphone → the preview re-acquires getUserMedia with that deviceId.
		var targetMic string
		if err := chromedp.Run(cctx, chromedp.Evaluate(`(() => {
			const sel = document.querySelector('.dc-mic-select');
			const cur = sel.value;
			return [...sel.options].map(o => o.value).find(v => v && v !== cur) || "";
		})()`, &targetMic)); err != nil {
			t.Fatalf("find an alternate mic: %v", err)
		}
		if targetMic == "" {
			t.Fatalf("expected fake media to expose more than one microphone to switch to")
		}
		pickMic := fmt.Sprintf(`(() => {
			const sel = document.querySelector('.dc-mic-select');
			sel.value = %q;
			sel.dispatchEvent(new Event('change', { bubbles: true }));
			return true;
		})()`, targetMic)
		audioIs := fmt.Sprintf(`(() => {
			const v = document.querySelector('.dc-video');
			if (!v || !v.srcObject) return false;
			const at = v.srcObject.getAudioTracks()[0];
			return !!at && at.getSettings().deviceId === %q;
		})()`, targetMic)
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(pickMic, nil),
			chromedp.Poll(audioIs, nil, chromedp.WithPollingTimeout(20*time.Second)),
		); err != nil {
			t.Fatalf("switching the mic did not re-acquire the chosen device: %v", err)
		}

		// Enter → the chosen mic persists into the stream published to the greenroom (AC-12);
		// the guest never has to re-pick once in session.
		sessionMicIs := fmt.Sprintf(`(() => {
			const v = document.querySelector('.gs-selfview');
			if (!v || !v.srcObject) return false;
			const at = v.srcObject.getAudioTracks()[0];
			return !!at && at.getSettings().deviceId === %q;
		})()`, targetMic)
		if err := chromedp.Run(cctx,
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
			chromedp.Poll(sessionMicIs, nil, chromedp.WithPollingTimeout(10*time.Second)),
		); err != nil {
			t.Fatalf("the selected mic must persist into the published session stream: %v", err)
		}
	})
}

// Race guard: if the guest changes a device and then hits Enter before the re-acquire finishes, the
// in-flight switch must NOT swap (and stop the tracks of) the stream the publisher just took live —
// that would put a dead/old stream on air. The switch's getUserMedia is delayed so it resolves AFTER
// entry has published; the published self-view tracks must still be LIVE once it does.
func TestDeviceCheck_SwitchDuringEntryDoesNotPublishStoppedStream(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-mic-select`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("preview did not render: %v", err)
		}

		// From here, delay every getUserMedia so the device switch's re-acquire resolves well after
		// the (fast, local) entry has published — opening the race window deterministically. The
		// initial capture already happened above, so only the switch is affected; it flags on resolve.
		inject := `(() => {
			const md = navigator.mediaDevices;
			const orig = md.getUserMedia.bind(md);
			window.__switchResolved = false;
			md.getUserMedia = (c) => new Promise((resolve, reject) => {
				setTimeout(() => { orig(c).then((str) => { window.__switchResolved = true; resolve(str); }, reject); }, 700);
			});
			return true;
		})()`
		var targetMic string
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(inject, nil),
			chromedp.Evaluate(`(() => {
				const sel = document.querySelector('.dc-mic-select');
				const cur = sel.value;
				return [...sel.options].map((o) => o.value).find((v) => v && v !== cur) || "";
			})()`, &targetMic),
		); err != nil {
			t.Fatalf("inject + pick alternate mic: %v", err)
		}
		if targetMic == "" {
			t.Fatalf("expected fake media to expose more than one microphone")
		}

		// Start the (now-delayed) switch, then immediately Enter — the publisher takes the current
		// stream live while the switch is still acquiring.
		switchThenEnter := fmt.Sprintf(`(() => {
			const sel = document.querySelector('.dc-mic-select');
			sel.value = %q;
			sel.dispatchEvent(new Event('change', { bubbles: true }));
			// Click Enter synchronously, in the SAME tick as the change — before Preact re-renders the
			// held/disabled Enter — to drive the sub-frame micro-race the code guard defends. (A real
			// user can't change-then-click inside one frame; the held Enter covers them, exercised by
			// TestDeviceCheck_EnterHeldDuringSwitchPublishesChosenDevice.)
			document.querySelector('.dc-enter').click();
			return true;
		})()`, targetMic)
		// Grab the entry-time track OBJECTS (the stream taken live) the instant we're entered, while
		// the switch is still acquiring — then assert THOSE objects stay live after it resolves. We
		// hold the track refs (not the element's srcObject) because a late switch re-points the
		// self-view to its new stream on re-render, which would otherwise mask the stopped publish.
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(switchThenEnter, nil),
			chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
			chromedp.Evaluate(`(() => {
				const so = document.querySelector('.gs-selfview').srcObject;
				window.__pub = { audio: so.getAudioTracks()[0], video: so.getVideoTracks()[0] };
				return !!(window.__pub.audio && window.__pub.video);
			})()`, nil),
			// Wait until the late switch has actually resolved — the moment the bug would swap +
			// stop the published stream — before checking the published tracks are still alive.
			chromedp.Poll(`window.__switchResolved === true`, nil, chromedp.WithPollingTimeout(20*time.Second)),
		); err != nil {
			t.Fatalf("switch-then-enter flow: %v", err)
		}

		var audioState, videoState string
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(`window.__pub.audio.readyState`, &audioState),
			chromedp.Evaluate(`window.__pub.video.readyState`, &videoState),
		); err != nil {
			t.Fatalf("read published track states: %v", err)
		}
		if audioState != "live" || videoState != "live" {
			t.Fatalf("a late device switch stopped the published stream: audio=%q video=%q (want both live)", audioState, videoState)
		}
	})
}

// When entry wins the switch-vs-enter race the picker keeps the prior stream live — so the saved
// selection (and the dropdown it drives) must be resynced to the device that stream actually uses,
// not left on the aborted pick. Otherwise the persisted choice (and the select after a network
// retry back to preview) names a device the live stream isn't using.
func TestDeviceCheck_AbortedSwitchResyncsPersistedDevice(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-mic-select`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("preview did not render: %v", err)
		}

		inject := `(() => {
			const md = navigator.mediaDevices;
			const orig = md.getUserMedia.bind(md);
			window.__switchResolved = false;
			md.getUserMedia = (c) => new Promise((resolve, reject) => {
				setTimeout(() => { orig(c).then((str) => { window.__switchResolved = true; resolve(str); }, reject); }, 700);
			});
			return true;
		})()`
		var targetMic string
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(inject, nil),
			chromedp.Evaluate(`(() => {
				const sel = document.querySelector('.dc-mic-select');
				const cur = sel.value;
				return [...sel.options].map((o) => o.value).find((v) => v && v !== cur) || "";
			})()`, &targetMic),
		); err != nil {
			t.Fatalf("inject + pick alternate mic: %v", err)
		}
		if targetMic == "" {
			t.Fatalf("expected fake media to expose more than one microphone")
		}

		switchThenEnter := fmt.Sprintf(`(() => {
			const sel = document.querySelector('.dc-mic-select');
			sel.value = %q;
			sel.dispatchEvent(new Event('change', { bubbles: true }));
			// Click Enter synchronously, in the SAME tick as the change — before Preact re-renders the
			// held/disabled Enter — to drive the sub-frame micro-race the code guard defends. (A real
			// user can't change-then-click inside one frame; the held Enter covers them, exercised by
			// TestDeviceCheck_EnterHeldDuringSwitchPublishesChosenDevice.)
			document.querySelector('.dc-enter').click();
			return true;
		})()`, targetMic)
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(switchThenEnter, nil),
			chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
			chromedp.Evaluate(`(() => {
				const so = document.querySelector('.gs-selfview').srcObject;
				window.__liveMic = so.getAudioTracks()[0].getSettings().deviceId;
				return !!window.__liveMic;
			})()`, nil),
			chromedp.Poll(`window.__switchResolved === true`, nil, chromedp.WithPollingTimeout(20*time.Second)),
		); err != nil {
			t.Fatalf("switch-then-enter flow: %v", err)
		}

		// The persisted mic must match the device the published stream is actually using — the
		// aborted pick (targetMic) must not survive in storage or drive the (re-shown) dropdown.
		var savedMic, liveMic string
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(`sessionStorage.getItem("gp.device.mic") || ""`, &savedMic),
			chromedp.Evaluate(`window.__liveMic`, &liveMic),
		); err != nil {
			t.Fatalf("read persisted vs live mic: %v", err)
		}
		if savedMic != liveMic {
			t.Fatalf("aborted switch left the picker desynced: saved mic=%q but live mic=%q (want equal)", savedMic, liveMic)
		}
		if savedMic == targetMic {
			t.Fatalf("the aborted pick %q must not persist as the selection", targetMic)
		}
	})
}

// The chosen device must be the one published: while a switch is still acquiring, Enter is held
// disabled, so a guest who picks a new mic and reaches for Enter goes live on THAT mic once the
// switch settles (not the prior one). Verifies the hold and that entry afterward publishes the pick.
func TestDeviceCheck_EnterHeldDuringSwitchPublishesChosenDevice(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-mic-select`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("preview did not render: %v", err)
		}

		// Delay the switch acquire so the "held" window is observable.
		inject := `(() => {
			const md = navigator.mediaDevices;
			const orig = md.getUserMedia.bind(md);
			window.__switchResolved = false;
			md.getUserMedia = (c) => new Promise((resolve, reject) => {
				setTimeout(() => { orig(c).then((str) => { window.__switchResolved = true; resolve(str); }, reject); }, 700);
			});
			return true;
		})()`
		var targetMic string
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(inject, nil),
			chromedp.Evaluate(`(() => {
				const sel = document.querySelector('.dc-mic-select');
				const cur = sel.value;
				return [...sel.options].map((o) => o.value).find((v) => v && v !== cur) || "";
			})()`, &targetMic),
		); err != nil {
			t.Fatalf("inject + pick alternate mic: %v", err)
		}
		if targetMic == "" {
			t.Fatalf("expected fake media to expose more than one microphone")
		}

		dispatch := fmt.Sprintf(`(() => {
			const sel = document.querySelector('.dc-mic-select');
			sel.value = %q;
			sel.dispatchEvent(new Event('change', { bubbles: true }));
			return true;
		})()`, targetMic)
		// Enter is held disabled while the switch acquires, then released once it settles.
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(dispatch, nil),
			chromedp.Poll(`document.querySelector('.dc-enter').disabled === true`,
				nil, chromedp.WithPollingTimeout(5*time.Second)),
			chromedp.Poll(`window.__switchResolved === true && document.querySelector('.dc-enter').disabled === false`,
				nil, chromedp.WithPollingTimeout(20*time.Second)),
		); err != nil {
			t.Fatalf("Enter was not held during the switch then released: %v", err)
		}

		// Entering after the switch settles publishes the CHOSEN mic, live.
		liveMicIs := fmt.Sprintf(`(() => {
			const v = document.querySelector('.gs-selfview');
			if (!v || !v.srcObject) return false;
			const at = v.srcObject.getAudioTracks()[0];
			return !!at && at.readyState === "live" && at.getSettings().deviceId === %q;
		})()`, targetMic)
		if err := chromedp.Run(cctx,
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
			chromedp.Poll(liveMicIs, nil, chromedp.WithPollingTimeout(10*time.Second)),
		); err != nil {
			t.Fatalf("after the switch settled, entry must publish the chosen mic live: %v", err)
		}
	})
}

// A device switch whose getUserMedia fails (unplugged / busy device) must not strand the guest on
// the error screen with the prior camera/mic still running behind it. The working preview stays
// live, the picker rolls back to the device actually in use, and a notice explains the no-op.
func TestDeviceCheck_SwitchFailureKeepsPreviewAndRollsBack(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-mic-select`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("preview did not render: %v", err)
		}

		// Remember the live mic + hold a ref to the working audio track, then make the NEXT acquire
		// (the switch) fail with a non-overconstrained error so getStream rethrows.
		var origMic, targetMic string
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(`document.querySelector('.dc-mic-select').value`, &origMic),
			chromedp.Evaluate(`(() => { window.__a = document.querySelector('.dc-video').srcObject.getAudioTracks()[0]; return true; })()`, nil),
			chromedp.Evaluate(`(() => {
				const md = navigator.mediaDevices;
				const orig = md.getUserMedia.bind(md);
				md.getUserMedia = (c) => { md.getUserMedia = orig; return Promise.reject(new DOMException("busy", "NotReadableError")); };
				return true;
			})()`, nil),
			chromedp.Evaluate(`(() => {
				const sel = document.querySelector('.dc-mic-select');
				const cur = sel.value;
				return [...sel.options].map((o) => o.value).find((v) => v && v !== cur) || "";
			})()`, &targetMic),
		); err != nil {
			t.Fatalf("setup failing switch: %v", err)
		}
		if targetMic == "" {
			t.Fatalf("expected fake media to expose more than one microphone")
		}

		// Pick the (doomed) device; the switch fails.
		pick := fmt.Sprintf(`(() => {
			const sel = document.querySelector('.dc-mic-select');
			sel.value = %q;
			sel.dispatchEvent(new Event('change', { bubbles: true }));
			return true;
		})()`, targetMic)
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(pick, nil),
			// The failure surfaces a notice and KEEPS the preview — it must not drop to the error screen.
			chromedp.WaitVisible(`.dc-switch-error`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("a failed switch must keep the preview and show a notice, not the error screen: %v", err)
		}

		var hasError, hasVideo bool
		var micNow, trackState string
		if err := chromedp.Run(cctx,
			chromedp.Evaluate(`!!document.querySelector('.dc-error')`, &hasError),
			chromedp.Evaluate(`!!document.querySelector('.dc-video')`, &hasVideo),
			chromedp.Evaluate(`document.querySelector('.dc-mic-select').value`, &micNow),
			chromedp.Evaluate(`window.__a.readyState`, &trackState),
		); err != nil {
			t.Fatalf("read post-failure state: %v", err)
		}
		if hasError || !hasVideo {
			t.Fatalf("a failed switch must stay in preview: errorScreen=%v videoPresent=%v", hasError, hasVideo)
		}
		if trackState != "live" {
			t.Fatalf("the working preview stream must stay live after a failed switch, got %q", trackState)
		}
		if micNow != origMic || micNow == targetMic {
			t.Fatalf("the failed pick must roll back to the working device: now=%q orig=%q failed=%q", micNow, origMic, targetMic)
		}
	})
}

// A failed entry must not leave the camera running behind the error UI: the preview track is
// released even when the entry POST fails (here forced by retiring the pass server-side).
func TestDeviceCheck_EntryFailureReleasesCamera(t *testing.T) {
	s := seedDeviceCheck(t)
	ctx := context.Background()

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		var trackState string
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Evaluate(`(() => { window.__dcTrack = document.querySelector('.dc-video').srcObject.getVideoTracks()[0]; return window.__dcTrack.readyState; })()`, &trackState),
		); err != nil {
			t.Fatalf("preview: %v", err)
		}
		if trackState != "live" {
			t.Fatalf("preview camera track = %q, want live", trackState)
		}

		// Retire the pass server-side so the entry POST fails (410).
		if err := s.store.SetPassStatus(ctx, s.passID, store.PassRevoked); err != nil {
			t.Fatalf("SetPassStatus: %v", err)
		}
		if err := chromedp.Run(cctx,
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-error`, chromedp.ByQuery),
			chromedp.Poll(`window.__dcTrack && window.__dcTrack.readyState === "ended"`,
				nil, chromedp.WithPollingTimeout(10*time.Second)),
		); err != nil {
			t.Fatalf("a failed entry must release the camera: %v", err)
		}
		if p, _ := s.store.GetPass(ctx, s.passID); p.Status == store.PassOpened {
			t.Fatalf("a failed entry must not mark opened, got %q", p.Status)
		}
	})
}

// T-7 / AC-6: a guest publishes its camera and a greenroom tile renders it over P2P, and an
// ICE restart (the per-tile Reconnect control) keeps the media flowing. Two real Chrome tabs in
// one browser exchange media over loopback with fake devices.
func TestPeerLink_GuestPublishesToHostMonitor(t *testing.T) {
	s := seedDeviceCheck(t)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()

	// Tab 1: the guest. Tab 2 (the host) is created in the SAME browser so loopback P2P works.
	guestCtx, cancelGuest := chromedp.NewContext(allocCtx)
	defer cancelGuest()
	guestCtx, cancelGuestT := context.WithTimeout(guestCtx, 150*time.Second)
	defer cancelGuestT()
	hostCtx, cancelHost := chromedp.NewContext(guestCtx)
	defer cancelHost()

	// Guest: open the magic link, run the device check, and enter → starts publishing.
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

	// Host: authenticate via the session cookie, open the greenroom → the grid consumes the
	// guest's camera over P2P and the tile shows live (fake-device) frames.
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.gr-video`, chromedp.ByQuery),
		chromedp.Poll(`!!document.querySelector('.gr-video') && document.querySelector('.gr-video').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("greenroom grid did not render the guest over P2P: %v", err)
	}

	// ICE restart: the per-tile Reconnect control re-offers with an ICE restart; media keeps flowing.
	if err := chromedp.Run(hostCtx,
		chromedp.Click(`.gr-reconnect`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.gr-video') && document.querySelector('.gr-video').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(30*time.Second)),
	); err != nil {
		t.Fatalf("media did not survive an ICE restart: %v", err)
	}
}
