//go:build browser

package browsertest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// publishGuest runs a guest tab through device-check → enter → publishing-live. `data-pub="live"`
// means the guest's publishing Room connected, i.e. it has joined the signaling room, so a
// rebind may name it as a slot occupant. label is used in errors instead of the token (EN-16).
func publishGuest(t *testing.T, ctx context.Context, base, rawToken, label string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/p/"+rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest %s publish flow: %v", label, err)
	}
}

// T-9 / AC-8 + AC-10 — the M2 capstone tracer. Real Chrome tabs for two guests, the host
// greenroom monitor, and the OBS cam source page exchange fake media over loopback. The host
// binds cam-1 to guest A (the OBS source renders A), then reassigns the slot LIVE to guest B;
// the OBS source re-routes to B's distinct stream with NO page reload and no OBS edit
// (slot-rebind/epoch, EN-1/EN-3). The reducer-level epoch/on-air invariants (epoch monotonic,
// stale-epoch on-air ignored, on-air reset on rebind) are proven in reducer_test.go.
func TestTracer_LiveSlotRebindReroutesSource(t *testing.T) {
	s := seedDeviceCheck(t)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()

	// Each peer runs as its OWN headless-Chrome instance, NOT as tabs in one browser. Two
	// guests both capturing the synthetic camera in a single browser contend on the one fake
	// device and the second publisher never goes live; giving each peer its own browser gives
	// it its own fake device. They connect as real P2P peers over loopback (127.0.0.1 host
	// candidates) — which also mirrors distinct machines more faithfully than co-located tabs.
	// Deriving every context from rootCtx (each created before its first Run gets its own
	// browser from the shared allocator) also gives them ONE shared deadline for the run.
	rootCtx, cancelRoot := chromedp.NewContext(allocCtx)
	defer cancelRoot()
	rootCtx, cancelDeadline := context.WithTimeout(rootCtx, 240*time.Second)
	defer cancelDeadline()
	guestACtx := rootCtx
	guestBCtx, cancelB := chromedp.NewContext(rootCtx)
	defer cancelB()
	obsCtx, cancelOBS := chromedp.NewContext(rootCtx)
	defer cancelOBS()
	hostCtx, cancelHost := chromedp.NewContext(rootCtx)
	defer cancelHost()

	// Both guests publish and join the room (so either can be named as a slot occupant).
	publishGuest(t, guestACtx, s.base, s.rawToken, "A")
	publishGuest(t, guestBCtx, s.base, s.rawTokenB, "B")

	// OBS cam source page: subscribed to cam-1, no occupant bound yet.
	if err := chromedp.Run(obsCtx,
		chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
		chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("obs source page did not load: %v", err)
	}

	// Host greenroom: the greenroom grid consumes guests over P2P (proves the host path), and
	// its socket carries our slot rebinds — a second host /ws would be evicted as a duplicate
	// identity, so the rebind must ride the greenroom tab's own connection (recorder-injected).
	injectRecorder := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(wsRecorderJS).Do(ctx)
		return err
	})
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hostCtx,
		injectRecorder,
		network.Enable(),
		setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(`.gr-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.gr-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("greenroom grid did not render a guest over P2P: %v", err)
	}

	rebind := func(occupant string) {
		t.Helper()
		var ok bool
		if err := chromedp.Run(hostCtx, chromedp.Evaluate(
			fmt.Sprintf(`window.__gpSendLastWS({t:"rebind",slot:%q,occupantPeerId:%q})`, s.slotLabel, occupant), &ok,
		)); err != nil {
			t.Fatalf("send rebind: %v", err)
		}
		if !ok {
			t.Fatalf("rebind not sent: no open host socket")
		}
	}

	// Bind cam-1 → guest A; the OBS source renders A. Capture A's received stream id, and stamp
	// the OBS document so a later check can prove the page was never reloaded. The stamp is set
	// with a one-shot Evaluate (NOT re-injected on navigation), so any reload wipes it.
	rebind(s.passID)
	var idA string
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(`!!document.querySelector('#obs-video') && document.querySelector('#obs-video').videoWidth > 0`,
			nil, chromedp.WithPollingTimeout(60*time.Second)),
		chromedp.Evaluate(`document.querySelector('#obs-video').srcObject.id`, &idA),
		chromedp.Evaluate(`(window.__gpObsLoadMark = "m1")`, nil),
	); err != nil {
		t.Fatalf("obs source did not render guest A: %v", err)
	}
	if idA == "" {
		t.Fatalf("guest A stream id empty — cannot prove a re-route")
	}

	// Reassign cam-1 → guest B LIVE. The OBS source re-routes to B's DISTINCT stream (slot-rebind
	// /epoch). The synthetic frames are identical across guests, so the received MediaStream id is
	// the witness that the occupant actually changed.
	rebind(s.passIDB)
	rerouted := fmt.Sprintf(`(() => {
		const v = document.querySelector('#obs-video');
		return !!v && v.videoWidth > 0 && v.srcObject && v.srcObject.id !== %q;
	})()`, idA)
	if err := chromedp.Run(obsCtx,
		chromedp.Poll(rerouted, nil, chromedp.WithPollingTimeout(60*time.Second)),
	); err != nil {
		t.Fatalf("OBS source did not re-route to guest B after a live slot-rebind (no OBS edit): %v", err)
	}

	// AC-8: the re-route happens with NO page reload / no OBS edit. The document we stamped is
	// still the live one — a reload (e.g. handling slot-rebind via location.reload()) would have
	// wiped the one-shot stamp, even though the reloaded page would also reconnect and render B.
	var sameDoc bool
	if err := chromedp.Run(obsCtx, chromedp.Evaluate(`window.__gpObsLoadMark === "m1"`, &sameDoc)); err != nil {
		t.Fatalf("read OBS document stamp: %v", err)
	}
	if !sameDoc {
		t.Fatalf("OBS source page reloaded across the rebind — AC-8 requires re-route with NO reload")
	}

	// A greenroom grid tile keeps rendering across the rebind (the rebind re-routes only the
	// OBS source, never the host grid).
	var hmLive bool
	if err := chromedp.Run(hostCtx, chromedp.Evaluate(
		`(() => { const v = document.querySelector('.gr-video'); return !!v && v.videoWidth > 0; })()`, &hmLive,
	)); err != nil {
		t.Fatalf("host grid check: %v", err)
	}
	if !hmLive {
		t.Fatalf("a host grid tile stopped rendering across the rebind")
	}
}
