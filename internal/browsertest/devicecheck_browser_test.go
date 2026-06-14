//go:build browser

package browsertest

import (
	"context"
	"io"
	"path/filepath"
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
	store      *store.Store
	base       string // server base URL
	rawToken   string // guest A's raw magic-link token (/p/{rawToken})
	passID     string // guest A's pass id (== A's signaling peer id)
	rawTokenB  string // guest B's raw magic-link token (a second guest, for live re-route)
	passIDB    string // guest B's pass id (== B's signaling peer id)
	hostCookie string // host session JWT for the gp_session cookie (/greenroom + host /ws)
	srcToken   string // cam-1 slot's raw source token (/s/{slotLabel}?token=…)
	slotLabel  string // the cam slot's signaling label ("cam-1")
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
	if _, err := st.CreateSlot(ctx, store.CreateSlotParams{
		HostID: host.ID, Kind: store.SlotCam, Idx: ptr(int64(1)), SourceTokenHash: hasher.Hash(srcRaw),
	}); err != nil {
		t.Fatalf("CreateSlot: %v", err)
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
		hostCookie: sess, srcToken: srcRaw, slotLabel: "cam-1",
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

// T-7 / AC-6: a guest publishes its camera and a host-monitor tile renders it over P2P, and
// an ICE restart (the Reconnect control) keeps the media flowing. Two real Chrome tabs in one
// browser exchange media over loopback with fake devices.
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

	// Host: authenticate via the session cookie, open the greenroom → the host-monitor
	// consumes the guest's camera over P2P and the tile shows live (fake-device) frames.
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.hm-tile`, chromedp.ByQuery),
		chromedp.Poll(`!!document.querySelector('.hm-tile') && document.querySelector('.hm-tile').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("host monitor did not render the guest over P2P: %v", err)
	}

	// ICE restart: the Reconnect control re-offers with an ICE restart; media keeps flowing.
	if err := chromedp.Run(hostCtx,
		chromedp.Click(`.hm-reconnect`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.hm-tile') && document.querySelector('.hm-tile').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(30*time.Second)),
	); err != nil {
		t.Fatalf("media did not survive an ICE restart: %v", err)
	}
}
