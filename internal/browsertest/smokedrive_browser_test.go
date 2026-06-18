//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/coder/websocket"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
	"github.com/rock3r/guest-pass/internal/web"
)

// TestSmokeDrive_MultiGuest is a HEADFUL, screenshot-capturing driver for the multi-guest behavioral
// smoke — so the owner never hand-juggles guests/OBS. It reuses the fake-media chromedp harness to
// spin up N guest publishers + the host greenroom + an OBS source page (all browser tabs, fake cam/
// mic, peers over loopback), binds a cam slot, and walks the multi-guest grid + RF-8 flow on screen:
// renders the grid, then force-no-cams a NON-cooperating guest from the host tile (the host tile AND
// the OBS source go black — receiver-side enforcement, RF-8), then releases. It saves a screenshot at
// each step and, when headful, holds the windows open so you can watch. It does NOT exercise the
// on-air pill (it never fires obsSourceActiveChanged) or degradation — the onair/degradation
// browser tests cover those headless in CI.
//
// It is NOT a CI test: it is SKIPPED unless SMOKE_DRIVE=1, runs HEADFUL by default (set
// SMOKE_HEADLESS=1 to capture the same screenshots without popping windows), and is normally driven
// via scripts/smoke-drive.sh. Knobs: SMOKE_GUESTS (default 3), SMOKE_WATCH_SEC (headful hold, default
// 45), SMOKE_SHOTS (screenshot dir, default <repo>/.smoke/drive-shots).
func TestSmokeDrive_MultiGuest(t *testing.T) {
	if os.Getenv("SMOKE_DRIVE") == "" {
		t.Skip("headful smoke driver — set SMOKE_DRIVE=1 (or run scripts/smoke-drive.sh)")
	}
	headful := os.Getenv("SMOKE_HEADLESS") == ""
	watch := envInt("SMOKE_WATCH_SEC", 45)
	// Every guest/host/OBS browser must outlive the whole run, including the headful watch hold, so
	// size the per-browser deadline = active-flow budget + the watch period (a fixed deadline could
	// otherwise cancel guest 1's context mid-watch on a long SMOKE_WATCH_SEC or slow P2P).
	browserTTL := 240 * time.Second
	if headful {
		browserTTL += time.Duration(watch) * time.Second
	}
	n := envInt("SMOKE_GUESTS", 3)
	if n < 1 {
		n = 1
	}
	shotsDir := os.Getenv("SMOKE_SHOTS")
	if shotsDir == "" {
		shotsDir = filepath.Join(repoRoot(t), ".smoke", "drive-shots")
	}
	if err := os.MkdirAll(shotsDir, 0o755); err != nil {
		t.Fatalf("screenshot dir: %v", err)
	}
	t.Logf("smoke driver: %d guests, headful=%t, screenshots -> %s", n, headful, shotsDir)

	s := seedDrive(t, n)

	// 1) Every guest publishes from its OWN fake-media browser (kept alive for the whole run).
	for i, raw := range s.rawTokens {
		gctx := driveBrowser(t, headful, browserTTL)
		acts := []chromedp.Action{
			chromedp.Navigate(s.base + "/p/" + raw),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
		}
		// Guest 1 is the RF-8 target: make it NON-cooperating (a WS wrapper strips its own self-locks,
		// so it keeps SENDING after a force) — then the host tile + OBS source going black genuinely
		// proves CONSUMER-side detach, not the publisher stopping at source. (Inject before navigate.)
		noncoop := ""
		if i == 0 {
			acts = append([]chromedp.Action{injectScript(nonCooperatingPublisherJS)}, acts...)
			noncoop = " (non-cooperating — RF-8 target)"
		}
		if err := chromedp.Run(gctx, acts...); err != nil {
			t.Fatalf("guest %d publish: %v", i+1, err)
		}
		t.Logf("  guest %d live%s", i+1, noncoop)
	}

	// 2) OBS source page (a browser tab) subscribed to cam-1 — unbound for now (placeholder).
	obs := driveBrowser(t, headful, browserTTL)
	if err := chromedp.Run(obs,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source page: %v", err)
	}

	// 3) Bind cam-1 -> guest 1 over a transient host /ws connection. Done BEFORE the greenroom opens:
	//    both are peer "host", and one-connection-per-identity (EN-16) would otherwise evict the
	//    greenroom page. The binding persists in room state after this host connection closes.
	hostConn := driveDialHost(t, s.base, s.hostCookie)
	writeFrame(t, hostConn, signaling.Frame{T: "rebind", Slot: s.slotLabel, OccupantPeerID: s.passIDs[0]})
	time.Sleep(500 * time.Millisecond)
	_ = hostConn.Close(websocket.StatusNormalClosure, "bound")
	if err := chromedp.Run(obs,
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("obs source did not render the bound guest: %v", err)
	}
	shot(t, obs, shotsDir, "01-obs-rendering")

	// 4) Host greenroom (now the live host connection) renders every guest tile over P2P.
	host := driveBrowser(t, headful, browserTTL)
	setCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	allTiles := fmt.Sprintf(`document.querySelectorAll('.gr-tile[data-role="guest"] .gr-video').length === %d && `+
		`[...document.querySelectorAll('.gr-tile[data-role="guest"] .gr-video')].every((v) => v.videoWidth > 0)`, n)
	if err := chromedp.Run(host,
		network.Enable(), setCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.Poll(allTiles, nil, chromedp.WithPollingTimeout(90*time.Second)),
	); err != nil {
		t.Fatalf("host greenroom did not render %d guest tiles: %v", n, err)
	}
	t.Logf("  greenroom renders %d guests", n)
	shot(t, host, shotsDir, "02-grid")
	beat(headful)

	// 5) RF-8: force-no-cam guest 1 FROM the host tile (the page's own socket). The host tile's video
	//    track AND the OBS source's video track must go black — receiver-side detach, independent of
	//    the guest (which keeps sending). Then release restores both.
	tile := `.gr-tile[data-guest="` + s.passIDs[0] + `"]`
	if err := chromedp.Run(host,
		chromedp.Click(tile+` .gr-force[data-kind="cam"]`, chromedp.ByQuery),
		chromedp.WaitVisible(tile+` .gr-lock`, chromedp.ByQuery),
		chromedp.Poll(trackEnabledIs(tile+` .gr-video`, "video", false), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("force-no-cam did not detach the guest's video on the host tile (RF-8): %v", err)
	}
	if err := chromedp.Run(obs,
		chromedp.Poll(trackEnabledIs("#obs-video", "video", false), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("force-no-cam did not detach the guest's video on the OBS source (RF-8): %v", err)
	}
	t.Logf("  RF-8: guest 1 forced off camera — host tile + OBS source detached")
	shot(t, host, shotsDir, "03-forced-host")
	shot(t, obs, shotsDir, "04-forced-obs")
	beat(headful)

	if err := chromedp.Run(host,
		chromedp.Click(tile+` .gr-release[data-kind="cam"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(tile+` .gr-lock`, chromedp.ByQuery),
		chromedp.Poll(trackEnabledIs(tile+` .gr-video`, "video", true), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("release did not restore the guest's video on the host tile: %v", err)
	}
	if err := chromedp.Run(obs,
		chromedp.Poll(trackEnabledIs("#obs-video", "video", true), nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("release did not restore the guest's video on the OBS source: %v", err)
	}
	t.Logf("  released — host tile + OBS source restored")
	shot(t, host, shotsDir, "05-released")

	t.Logf("✅ smoke driver passed. Screenshots in %s", shotsDir)
	if headful {
		t.Logf("holding the windows open for %ds so you can watch (SMOKE_WATCH_SEC) …", watch)
		time.Sleep(time.Duration(watch) * time.Second)
	}
}

// TestSmokeDrive_Screenshare is a HEADFUL driver for the M4 screenshare media path (D-21/AC-11/12) —
// so the owner doesn't hand-juggle a sharer + host + viewer. It auto-drives: guest 1 shares its
// screen (a stubbed, animated getDisplayMedia canvas — no real picker), the host greenroom shows it
// in the preview rail and the driver clicks "Put live", and guest 2 renders the live share — all on
// screen, with a screenshot at each step. It then PRINTS the /s/screen?token=… URL and holds the
// windows open so the owner pastes it into a real OBS Browser Source and confirms OBS-CEF renders the
// moving test pattern (the one genuinely-manual gate; everything else here is what the chromedp T-12
// proves headless in CI).
//
// SKIPPED unless SMOKE_DRIVE=1; HEADFUL by default (SMOKE_HEADLESS=1 for screenshots only); normally
// run via scripts/smoke-drive.sh --screenshare. Knobs: SMOKE_WATCH_SEC (headful hold, default 180 —
// long enough to set up OBS), SMOKE_SHOTS (screenshot dir).
func TestSmokeDrive_Screenshare(t *testing.T) {
	if os.Getenv("SMOKE_DRIVE") == "" {
		t.Skip("headful smoke driver — set SMOKE_DRIVE=1 (or run scripts/smoke-drive.sh --screenshare)")
	}
	headful := os.Getenv("SMOKE_HEADLESS") == ""
	watch := envInt("SMOKE_WATCH_SEC", 180)
	browserTTL := 300 * time.Second
	if headful {
		browserTTL += time.Duration(watch) * time.Second
	}
	shotsDir := os.Getenv("SMOKE_SHOTS")
	if shotsDir == "" {
		shotsDir = filepath.Join(repoRoot(t), ".smoke", "drive-shots")
	}
	if err := os.MkdirAll(shotsDir, 0o755); err != nil {
		t.Fatalf("screenshot dir: %v", err)
	}
	s := seedDrive(t, 2) // guest 1 = sharer, guest 2 = viewer (both screenshare-eligible)

	enter := func(raw string) []chromedp.Action {
		return []chromedp.Action{
			chromedp.Navigate(s.base + "/p/" + raw),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
		}
	}

	// 1) Guest 1 enters and shares its screen (getDisplayMedia stubbed → animated canvas, injected
	//    before any page script, so there's no real OS screen-picker to click).
	sharer := driveBrowser(t, headful, browserTTL)
	if err := chromedp.Run(sharer, append([]chromedp.Action{injectScript(getDisplayMediaStubJS)},
		append(enter(s.rawTokens[0]),
			chromedp.WaitVisible(`.gs-screen[data-screen-state="idle"]`, chromedp.ByQuery),
			chromedp.Click(`.gs-screen-toggle`, chromedp.ByQuery),
			chromedp.WaitVisible(`.gs-screen[data-screen-state="backstage"]`, chromedp.ByQuery),
		)...)...); err != nil {
		t.Fatalf("guest 1 share: %v", err)
	}
	t.Logf("  guest 1 sharing (backstage)")

	// 2) Guest 2 enters (it will render the live share once the host selects it).
	viewer := driveBrowser(t, headful, browserTTL)
	if err := chromedp.Run(viewer, enter(s.rawTokens[1])...); err != nil {
		t.Fatalf("guest 2 enter: %v", err)
	}

	// 3) Host greenroom: the preview rail shows guest 1's screen over P2P.
	host := driveBrowser(t, headful, browserTTL)
	setCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	railA := `.gr-screen-tile[data-sharer="` + s.passIDs[0] + `"]`
	if err := chromedp.Run(host,
		network.Enable(), setCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(railA, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('`+railA+` .gr-screen-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("host rail did not render guest 1's screen: %v", err)
	}
	t.Logf("  host preview rail renders the share")
	shot(t, host, shotsDir, "screen-01-rail")
	beat(headful)

	// 4) Host puts it live → the tile is badged live, and guest 2 renders the live share for everyone.
	if err := chromedp.Run(host,
		chromedp.Click(railA+` .gr-screen-select`, chromedp.ByQuery),
		chromedp.WaitVisible(railA+`[data-live="1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("select-live: %v", err)
	}
	if err := chromedp.Run(viewer,
		chromedp.Poll(`!!document.querySelector('.gs-livescreen[data-sharer="`+s.passIDs[0]+`"] .gs-livescreen-video') && document.querySelector('.gs-livescreen[data-sharer="`+s.passIDs[0]+`"] .gs-livescreen-video').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("guest 2 did not render the live share (AC-11 everyone): %v", err)
	}
	t.Logf("  host selected it live; guest 2 renders the live share")
	shot(t, host, shotsDir, "screen-02-host-live")
	shot(t, viewer, shotsDir, "screen-03-viewer-live")

	// 5) The one MANUAL gate: a real OBS Browser Source on /s/screen renders the live share (CEF over
	//    the screen channel). Print the URL and hold the windows open while the owner connects OBS.
	screenURL := s.base + "/s/screen?token=" + s.srcTokenScreen
	t.Logf("✅ automated screenshare flow passed (rail → live → everyone). Screenshots in %s", shotsDir)
	t.Logf("──────────────────────────────────────────────────────────────────────")
	t.Logf(" OBS STEP — add an OBS Browser Source with this URL; it should render a moving test pattern:")
	t.Logf("   %s", screenURL)
	t.Logf("   (append &name=1 to also show the sharer's nameplate.)")
	t.Logf("   Holding the windows + server open for %ds so you can set up OBS …", watch)
	t.Logf("──────────────────────────────────────────────────────────────────────")
	if headful {
		time.Sleep(time.Duration(watch) * time.Second)
	}
}

// beat pauses briefly between visible transitions so a human watching headful can follow along.
func beat(headful bool) {
	if headful {
		time.Sleep(3 * time.Second)
	}
}

// envInt reads an integer env var, falling back to def when unset/invalid.
func envInt(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil {
		return v
	}
	return def
}

// driveAllocOpts builds the chromedp exec options for the driver: fake cam/mic + autoplay (like
// fakeMediaAllocOpts) but HEADFUL by default (Headless omitted) so the owner can watch; SMOKE_HEADLESS
// adds Headless to capture the same screenshots without windows.
func driveAllocOpts(headful bool) []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.NoSandbox,
		chromedp.Flag("use-fake-device-for-media-stream", true),
		chromedp.Flag("use-fake-ui-for-media-stream", true),
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
	}
	if !headful {
		opts = append(opts, chromedp.Headless, chromedp.DisableGPU)
	}
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	return opts
}

// driveBrowser starts one fake-media chromedp browser whose lifetime is tied to the test (cancels
// registered with t.Cleanup), so every guest/host/OBS window stays alive for the whole run.
func driveBrowser(t *testing.T, headful bool, timeout time.Duration) context.Context {
	t.Helper()
	alloc, cancelA := chromedp.NewExecAllocator(context.Background(), driveAllocOpts(headful)...)
	t.Cleanup(cancelA)
	ctx, cancelC := chromedp.NewContext(alloc)
	t.Cleanup(cancelC)
	ctx, cancelT := context.WithTimeout(ctx, timeout)
	t.Cleanup(cancelT)
	return ctx
}

// driveDialHost opens a host-authenticated /ws connection (the session cookie) for the slot rebind.
func driveDialHost(t *testing.T, base, cookie string) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(base, "http", "ws", 1) + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {(&http.Cookie{Name: auth.SessionCookie, Value: cookie}).String()}},
	})
	if err != nil {
		t.Fatalf("host /ws dial: %v", err)
	}
	return c
}

// shot saves a viewport screenshot to <dir>/<name>.png (best-effort; logged).
func shot(t *testing.T, ctx context.Context, dir, name string) {
	t.Helper()
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		t.Logf("  screenshot %s failed: %v", name, err)
		return
	}
	p := filepath.Join(dir, name+".png")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Logf("  write %s failed: %v", p, err)
		return
	}
	t.Logf("  📸 %s", p)
}

// driveSeed is a host + N guest passes + a cam-1 slot + a host session, for the headful driver.
type driveSeed struct {
	base           string
	hostCookie     string
	rawTokens      []string
	passIDs        []string
	srcToken       string
	slotLabel      string
	srcTokenScreen string // the screenshare slot's source token (/s/screen?token=…) for the screenshare driver
}

// seedDrive creates the fixtures for the headful driver: one live stream, n guest passes (each
// screenshare-eligible), a cam-1 slot, and the shared screenshare slot — each slot with its own
// source token (mirrors seedGrid + the slots from seedDeviceCheck).
func seedDrive(t *testing.T, n int) *driveSeed {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "drive.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	host, err := st.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "drive-sub", Email: "host@example.com", Name: "Host", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	stream, err := st.CreateStream(ctx, store.CreateStreamParams{HostID: host.ID, Title: "Drive Stream"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	hasher, err := token.NewHasher("smokedrive-browser-token-secret-dddddddd")
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}

	raws := make([]string, 0, n)
	passIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw, err := token.Mint()
		if err != nil {
			t.Fatalf("mint guest %d: %v", i, err)
		}
		pass, err := st.CreatePass(ctx, store.CreatePassParams{
			StreamID: stream.ID, Name: ptr(fmt.Sprintf("Guest %d", i+1)),
			Role: store.RoleGuest, TokenHash: hasher.Hash(raw), Status: store.PassSent,
		})
		if err != nil {
			t.Fatalf("CreatePass %d: %v", i, err)
		}
		// Screenshare-eligible (can_screen) so the screenshare driver can have a guest share (EN-23);
		// harmless to the multi-guest/RF-8 driver, which never opens the share affordance.
		if err := st.SetPassCanScreen(ctx, pass.ID, true); err != nil {
			t.Fatalf("SetPassCanScreen %d: %v", i, err)
		}
		raws = append(raws, raw)
		passIDs = append(passIDs, pass.ID)
	}

	srcRaw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint src: %v", err)
	}
	if _, err := st.CreateSlot(ctx, store.CreateSlotParams{
		HostID: host.ID, Kind: store.SlotCam, Idx: ptr(int64(1)), SourceTokenHash: hasher.Hash(srcRaw),
	}); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	// The shared screenshare slot (signaling label "screen") with its own source token, so the
	// screenshare driver can hand the owner a /s/screen?token=… URL for a real OBS Browser Source.
	srcScreenRaw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint screen src: %v", err)
	}
	if _, err := st.CreateSlot(ctx, store.CreateSlotParams{
		HostID: host.ID, Kind: store.SlotScreenshare, SourceTokenHash: hasher.Hash(srcScreenRaw),
	}); err != nil {
		t.Fatalf("CreateSlot screenshare: %v", err)
	}

	ring, err := auth.NewKeyRing("smokedrive-browser-session-secret-eeeeeeee")
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
	return &driveSeed{
		base: Serve(t, handler).URL, hostCookie: sess,
		rawTokens: raws, passIDs: passIDs, srcToken: srcRaw, slotLabel: "cam-1",
		srcTokenScreen: srcScreenRaw,
	}
}
