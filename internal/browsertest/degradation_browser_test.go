//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// statsOverrideJS wraps RTCRtpSender.getStats so a test can force a qualityLimitationReason on
// demand (window.__gpForceLimit = "cpu" | "bandwidth"); when unset it reports a CLEAN "none" rather
// than the real stats, because loopback connections report spurious "bandwidth" limitations (an
// artifact the design discounts) that would otherwise block the recovery assertion. Injected before
// any page script runs so the degradation sampler sees it.
const statsOverrideJS = `
(() => {
  RTCRtpSender.prototype.getStats = async function () {
    const limit = window.__gpForceLimit || "none";
    return new Map([
      ["o", { type: "outbound-rtp", qualityLimitationReason: limit }],
      ["r", { type: "remote-inbound-rtp", roundTripTime: 0.05, fractionLost: 0 }],
    ]);
  };
})();
`

// T-13 (unit): the PURE shedding ladder, exercised directly in the browser (no media), via the
// window.__gpDeg debug seam. Covers cpu shed order + program protection, the peer-leave recovery
// clamp (the crash repro), bandwidth param stepping, and recover-slow hysteresis.
func TestDegradation_PlanLadderUnit(t *testing.T) {
	s := seedDeviceCheck(t)
	Chrome(t, 60*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.Poll(`typeof (window.__gpDeg && window.__gpDeg.planLadder) === "function"`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("degradation ladder helpers not exposed: %v", err)
		}
		check := func(what, expr string) {
			var ok bool
			if err := chromedp.Run(cctx, chromedp.Evaluate(expr, &ok)); err != nil || !ok {
				t.Fatalf("planLadder %s: ok=%v err=%v", what, ok, err)
			}
		}
		// cpu sheds the lowest-priority thumbnail, NEVER the protected program.
		check("cpu shed + protect", `(() => {
			const r = window.__gpDeg.planLadder({reason:"cpu",
				senders:[{key:"mesh:a",priority:1},{key:"pub:h",priority:3,protected:true}],
				state:{cpuLevel:0,bw:{},recoverStreak:0,lastReason:null}});
			return r.disabled.indexOf("mesh:a")>=0 && r.disabled.indexOf("pub:h")<0
				&& r.degraded && r.degraded.dir==="lowering" && r.degraded.reason==="cpu";
		})()`)
		// Peer-leave clamp: a shed thumbnail peer left (only the protected sender remains) — the
		// carried cpuLevel must clamp instead of indexing past the shrunken sheddable list (no throw).
		check("peer-leave clamp", `(() => {
			try {
				const r = window.__gpDeg.planLadder({reason:null,
					senders:[{key:"pub:h",priority:3,protected:true}],
					state:{cpuLevel:1,bw:{},recoverStreak:2,lastReason:"cpu"}});
				return r.degraded===null && r.disabled.length===0;
			} catch (e) { return false; }
		})()`)
		// bandwidth lowers the constrained link's PARAMS (res first), not active:false.
		check("bandwidth params", `(() => {
			const r = window.__gpDeg.planLadder({reason:"bandwidth",
				senders:[{key:"mesh:a",priority:1}],
				state:{cpuLevel:0,bw:{},recoverStreak:0,lastReason:null}});
			return r.actions.some((a)=>a.params && a.params.scaleResolutionDownBy>=2)
				&& r.degraded && r.degraded.reason==="bandwidth";
		})()`)
		// recover-slow hysteresis: one clean sample after shedding does NOT recover yet.
		check("hysteresis hold", `(() => {
			const r = window.__gpDeg.planLadder({reason:null,
				senders:[{key:"mesh:a",priority:1}],
				state:{cpuLevel:1,bw:{},recoverStreak:0,lastReason:"cpu"}});
			return r.actions.length===0 && r.state.recoverStreak===1 && r.degraded && r.degraded.dir==="lowering";
		})()`)
	})
}

// T-13 / AC-14: per-publisher degradation. A guest meshes with another (so it has a sender to
// shed), then a forced cpu limitation makes its DegradationController shed the lowest-priority
// (backstage-thumbnail) sender via setParameters and self-report {t:stats,degraded:lowering/cpu};
// that round-trips through the server's fold into the guest's OWN degradation state. Clearing the
// limitation recovers (degrade-fast / recover-slow hysteresis). The server never sees media (D-23) —
// these are the publisher's own numbers.
func TestDegradation_ShedsAndReportsOnCpu(t *testing.T) {
	s := seedGrid(t, 2)

	// Guest B: a normal backstage peer, so guest A has a live mesh sender to degrade.
	enterGuestSession(t, s.base, s.rawTokens[1], "B")

	// Guest A: inject the getStats override, then enter.
	aAlloc, cancelAA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAA()
	aCtx, cancelA := chromedp.NewContext(aAlloc)
	defer cancelA()
	aCtx, cancelAT := context.WithTimeout(aCtx, 150*time.Second)
	defer cancelAT()
	injectStats := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(statsOverrideJS).Do(ctx)
		return err
	})
	if err := chromedp.Run(aCtx,
		injectStats,
		chromedp.Navigate(s.base+"/p/"+s.rawTokens[0]),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"] .gs-selfview`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest A enter: %v", err)
	}

	// A meshes with B → there is a thumbnail (sheddable) sender.
	if err := chromedp.Run(aCtx, chromedp.Poll(
		`document.querySelectorAll('.gr-tile .gr-video').length >= 1`,
		nil, chromedp.WithPollingTimeout(60*time.Second))); err != nil {
		t.Fatalf("guest A did not mesh with B: %v", err)
	}

	// Host: greenroom consumes A over the Publisher, so A also has a PROTECTED program/monitor sender
	// (priority 3) that cpu shedding must never hard-disable.
	hAlloc, cancelHA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelHA()
	hCtx, cancelH := chromedp.NewContext(hAlloc)
	defer cancelH()
	hCtx, cancelHT := context.WithTimeout(hCtx, 150*time.Second)
	defer cancelHT()
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	if err := chromedp.Run(hCtx,
		network.Enable(), setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.Poll(`[...document.querySelectorAll('.gr-tile .gr-video')].some((v) => v.videoWidth > 0)`,
			nil, chromedp.WithPollingTimeout(90*time.Second)),
	); err != nil {
		t.Fatalf("host greenroom did not consume guest A over the Publisher: %v", err)
	}

	// Force cpu pressure → shed the lowest-priority (mesh) sender + report lowering/cpu, round-tripped.
	if err := chromedp.Run(aCtx, chromedp.Evaluate(`window.__gpForceLimit = "cpu"`, nil)); err != nil {
		t.Fatalf("force cpu: %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.Poll(
		`document.querySelector('[data-entered]').dataset.degraded === "lowering:cpu"`,
		nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("cpu pressure did not round-trip into the guest's own degradation (want lowering:cpu): %v", err)
	}
	// cpu disables the lowest-priority MESH (thumbnail) sender, and NEVER the protected program/
	// monitor publish path — disabling that would kill the broadcast (DESIGN ladder).
	if err := chromedp.Run(aCtx, chromedp.Poll(
		`(() => {
			const d = (window.__gpDegradation || {}).disabled || [];
			return d.some((k) => k.indexOf("mesh:") === 0) && !d.some((k) => k.indexOf("pub:") === 0);
		})()`,
		nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("cpu shedding must disable the mesh thumbnail but PROTECT the program/monitor sender: %v", err)
	}

	// Clear the pressure → recover-slow hysteresis eventually clears the degradation.
	if err := chromedp.Run(aCtx, chromedp.Evaluate(`window.__gpForceLimit = null`, nil)); err != nil {
		t.Fatalf("clear limit: %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.Poll(
		`document.querySelector('[data-entered]').dataset.degraded === ""`,
		nil, chromedp.WithPollingTimeout(40*time.Second))); err != nil {
		t.Fatalf("the guest did not recover after the cpu pressure cleared: %v", err)
	}

	// Bandwidth pressure degrades the constrained link's PARAMS (res→fps→bitrate) rather than
	// disabling the encoder — a different shedding action for a different reason (T-13).
	if err := chromedp.Run(aCtx, chromedp.Evaluate(`window.__gpForceLimit = "bandwidth"`, nil)); err != nil {
		t.Fatalf("force bandwidth: %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.Poll(
		`document.querySelector('[data-entered]').dataset.degraded === "lowering:bandwidth"`,
		nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("bandwidth pressure did not round-trip (want lowering:bandwidth): %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.Poll(
		`(((window.__gpDegradation || {}).actions) || []).some((a) => a.params && a.params.scaleResolutionDownBy >= 2)`,
		nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("bandwidth shedding did not lower the constrained sender's resolution params: %v", err)
	}
}
