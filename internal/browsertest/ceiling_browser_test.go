//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// publishGuestWithSource is the shared setup for the ceiling tests: a guest (Dana) publishes (tab 1)
// and an OBS cam source (tab 2, same browser for loopback P2P) attaches to cam-1 with the given
// extra source-URL params; the host (live) binds cam-1 to the guest so the source renders the guest
// — creating the guest's PROGRAM sender (pub:<source>) that the quality ceiling caps. Returns the
// two contexts.
func publishGuestWithSource(t *testing.T, s *devSeed, srcParams string) (guestCtx, obsCtx context.Context) {
	t.Helper()
	if _, err := s.store.StartSession(context.Background(), s.streamID, s.hostID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	t.Cleanup(cancelAlloc)
	gctx, cancelGuest := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelGuest)
	gctx, cancelGuestT := context.WithTimeout(gctx, 180*time.Second)
	t.Cleanup(cancelGuestT)
	octx, cancelOBS := chromedp.NewContext(gctx)
	t.Cleanup(cancelOBS)

	if err := chromedp.Run(gctx,
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

	url := s.base + "/s/" + s.slotLabel + "?token=" + s.srcToken
	if srcParams != "" {
		url += "&" + srcParams
	}
	if err := chromedp.Run(octx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source page did not load: %v", err)
	}

	// Bind cam-1 to the guest over the host REST control → the source renders the guest's camera,
	// creating the program sender the ceiling caps.
	putHostJSON(t, s, "/api/passes/"+s.passID+"/slot", `{"slot":"cam-1"}`)
	if err := chromedp.Run(octx,
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("obs source did not render the bound occupant: %v", err)
	}
	return gctx, octx
}

// T-8 / AC-8 (D-19): the program encoder is actually capped at the stream ceiling, and a live host
// adjustment re-caps it. After the source connects, the guest's program sender (pub:<source>) runs
// at the ceiling's maxBitrate/maxFramerate; lowering the ceiling live lowers the sender.
func TestCeiling_CapsProgramSenderLive(t *testing.T) {
	s := seedDeviceCheck(t)
	guestCtx, _ := publishGuestWithSource(t, s, "")

	// The seeded stream carries the default ceiling (720/30/2500) → the program sender is capped at it.
	if err := chromedp.Run(guestCtx, chromedp.Poll(
		`(window.__gpPubEncodings ? window.__gpPubEncodings() : []).some((e) => e.maxBitrate === 2500000 && e.maxFramerate === 30)`,
		nil, chromedp.WithPollingTimeout(20*time.Second),
	)); err != nil {
		t.Fatalf("program sender was not capped at the default ceiling (2500kbps/30fps): %v", err)
	}

	// Host lowers the ceiling live → the program sender re-caps to the new values.
	putHostJSON(t, s, "/api/streams/"+s.streamID+"/ceiling", `{"maxRes":480,"maxFps":20,"maxBitrateKbps":800}`)
	if err := chromedp.Run(guestCtx, chromedp.Poll(
		`(window.__gpPubEncodings ? window.__gpPubEncodings() : []).some((e) => e.maxBitrate === 800000 && e.maxFramerate === 20)`,
		nil, chromedp.WithPollingTimeout(20*time.Second),
	)); err != nil {
		t.Fatalf("program sender did not re-cap to the lowered ceiling (800kbps/20fps): %v", err)
	}
}

// T-8 / AC-8 (D-19): a per-source program-resolution override (the source's ?res URL param) caps the
// occupant's sender feeding THAT source tighter than the stream ceiling — the program sender is
// downscaled (scaleResolutionDownBy > 1) relative to the capture, which a no-override source is not.
func TestCeiling_PerSourceResolutionOverride(t *testing.T) {
	s := seedDeviceCheck(t)
	guestCtx, obsCtx := publishGuestWithSource(t, s, "res=240")

	// With ?res=240 below the fake capture height, the program sender is downscaled (scale > 1).
	if err := chromedp.Run(guestCtx, chromedp.Poll(
		`(window.__gpPubEncodings ? window.__gpPubEncodings() : []).some((e) => typeof e.scaleResolutionDownBy === 'number' && e.scaleResolutionDownBy > 1.2)`,
		nil, chromedp.WithPollingTimeout(20*time.Second),
	)); err != nil {
		t.Fatalf("per-source ?res override did not downscale the program sender: %v", err)
	}

	// Reload the SAME source WITHOUT ?res (host removed it): the source sends res:0 on rebind, which
	// CLEARS the stale override so the program sender returns to the stream ceiling (no downscale on a
	// sub-720 capture) — codex/Bugbot: a dropped ?res must not leave a stale tighter cap.
	if err := chromedp.Run(obsCtx,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('#obs-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("source reload without ?res did not re-render: %v", err)
	}
	if err := chromedp.Run(guestCtx, chromedp.Poll(
		`(window.__gpPubEncodings ? window.__gpPubEncodings() : []).every((e) => !(e.scaleResolutionDownBy > 1.2))`,
		nil, chromedp.WithPollingTimeout(20*time.Second),
	)); err != nil {
		t.Fatalf("dropping ?res did not clear the stale per-source override: %v", err)
	}
}

// AC-8: the host adjusts the ceiling from the greenroom. With a live session, the host's greenroom
// shows the quality-ceiling control populated from GET /api/session/ceiling; changing a value +
// Apply PUTs it and persists streams.max_*. (The publisher-side cap is the other tests; this proves
// the host-facing affordance is wired end to end.)
func TestCeiling_GreenroomControlApplies(t *testing.T) {
	s := seedDeviceCheck(t)
	ctx := context.Background()
	if _, err := s.store.StartSession(ctx, s.streamID, s.hostID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	hostCtx, cancelHost := chromedp.NewContext(allocCtx)
	defer cancelHost()
	hostCtx, cancelHostT := context.WithTimeout(hostCtx, 60*time.Second)
	defer cancelHostT()

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		openPeopleRail(),
		// The control appears in the host's persistent right rail (GET populated it) with the
		// stream's default ceiling; it must not return to the monitoring-grid utility bar.
		chromedp.WaitVisible(`.gr-people-rail .gr-ceiling-res`, chromedp.ByQuery),
		chromedp.Poll(`!!document.querySelector('.gr-people-rail .gr-ceiling-res') && !document.querySelector('.gr-main-stage .gr-ceiling-res')`, nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.WaitVisible(`.gr-ceiling-res`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.gr-ceiling-res').value === '720'`, nil, chromedp.WithPollingTimeout(15*time.Second)),
		// Lower the resolution and apply.
		chromedp.SetValue(`.gr-ceiling-res`, "480", chromedp.ByQuery),
		chromedp.Click(`.gr-ceiling-apply`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("greenroom ceiling control flow: %v", err)
	}

	// The PUT persisted the new ceiling.
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _ := s.store.GetStream(ctx, s.streamID)
		if st != nil && st.MaxRes != nil && *st.MaxRes == 480 {
			break
		}
		if time.Now().After(deadline) {
			st, _ := s.store.GetStream(ctx, s.streamID)
			t.Fatalf("greenroom Apply did not persist the ceiling; max_res = %v, want 480", st.MaxRes)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// AC-8 (codex P2): a RECREATED program sender (same pub:<id> key, new RTCRtpSender on reconnect)
// must be re-capped — the ceiling memo is keyed by the sender OBJECT, not the key, so a fresh sender
// with default encodings can't run above the ceiling until the host next touches it.
func TestCeiling_RecapsRecreatedSender(t *testing.T) {
	s := seedDeviceCheck(t)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 60*time.Second)
	defer cancelT()

	if err := chromedp.Run(ctx,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Poll(`!!(window.__gpDeg && window.__gpDeg.DegradationController)`, nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("degradation seam not available: %v", err)
	}

	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const mkSender = () => {
			const s = { enc: {}, track: { kind: "video", getSettings: () => ({ height: 720 }) } };
			s.getParameters = () => ({ encodings: [ { ...s.enc } ] });
			s.setParameters = (p) => { s.enc = p.encodings[0]; return Promise.resolve(); };
			return s;
		};
		let sender = mkSender();
		const deg = new window.__gpDeg.DegradationController({
			getTargets: () => [{ key: "pub:x", priority: 3, protected: true, sender }],
			report: () => {},
		});
		deg.setCeiling({ maxRes: 360, maxFps: 20, maxBitrateKbps: 800 }); // caps sender A → scale 2
		const cappedA = Math.abs(sender.enc.scaleResolutionDownBy - 2) < 0.01 && sender.enc.maxBitrate === 800000;
		// Reconnect: a NEW sender object reuses the same pub:x key with DEFAULT (uncapped) encodings.
		sender = mkSender();
		deg._enforceCeiling();
		const cappedB = Math.abs(sender.enc.scaleResolutionDownBy - 2) < 0.01 && sender.enc.maxBitrate === 800000;
		return cappedA && cappedB;
	})()`, &ok)); err != nil {
		t.Fatalf("recreated-sender eval: %v", err)
	}
	if !ok {
		t.Fatalf("a recreated program sender was not re-capped at the ceiling")
	}
}

// T-8 / AC-8 (D-19): the degradation recover path clamps to the ceiling — "bump quality now"
// (recoverNow) restores to the ceiling baseline, NEVER above it. Driven at the controller unit level
// via the __gpDeg seam with a fake sender so the clamp is deterministic (no real media/pressure).
func TestCeiling_RecoverClampsToCeiling(t *testing.T) {
	s := seedDeviceCheck(t)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 60*time.Second)
	defer cancelT()

	// Load any page that bundles the rtc seam (the OBS source page imports degradation indirectly via
	// the app bundle; use the device-check island page which pulls in __gpDeg).
	if err := chromedp.Run(ctx,
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Poll(`!!(window.__gpDeg && window.__gpDeg.DegradationController)`, nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("degradation seam not available: %v", err)
	}

	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		// A fake 720p-capture program sender recording the last encoding setParameters applied.
		let enc = {};
		const sender = {
			track: { kind: "video", getSettings: () => ({ height: 720 }) },
			getParameters: () => ({ encodings: [ { ...enc } ] }),
			setParameters: (p) => { enc = p.encodings[0]; return Promise.resolve(); },
			getStats: () => Promise.resolve(new Map()),
		};
		const deg = new window.__gpDeg.DegradationController({
			getTargets: () => [{ key: "pub:x", priority: 3, protected: true, sender }],
			report: () => {},
		});
		// Ceiling 360p/20fps/800kbps on a 720p capture → scaleResolutionDownBy 2.
		deg.setCeiling({ maxRes: 360, maxFps: 20, maxBitrateKbps: 800 });
		// Simulate a deep shed (well below the ceiling), then "bump quality now".
		sender.setParameters({ encodings: [{ scaleResolutionDownBy: 8, maxFramerate: 5, maxBitrate: 100000 }] });
		deg.recoverNow();
		// recoverNow must restore to the CEILING, never to full quality (scaleResolutionDownBy 1 /
		// uncapped bitrate). Allow float wobble on the scale.
		return Math.abs(enc.scaleResolutionDownBy - 2) < 0.01 && enc.maxFramerate === 20 && enc.maxBitrate === 800000;
	})()`, &ok)); err != nil {
		t.Fatalf("recover-clamp eval: %v", err)
	}
	if !ok {
		t.Fatalf("recoverNow did not clamp to the ceiling (expected scale 2 / 20fps / 800kbps)")
	}
}
