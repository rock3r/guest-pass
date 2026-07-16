//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
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

// T-14 (race): the host "bump quality now" override must cancel an in-flight sample. recoverNow()
// can land while an async sample() is suspended at `await getStats()`; when that pass resumes it must
// NOT re-apply its (now-stale, pre-recovery) shedding plan or emit a stale {t:stats} that undoes the
// override (Bugbot: "recover-now races in-flight sample"). Driven deterministically through a gated
// getStats — no timers, no timing race: we start a sample, hold it mid-pass, fire recoverNow, then
// release the read and assert the resuming pass made no shedding/report.
func TestDegradation_RecoverNowCancelsInFlightSample(t *testing.T) {
	s := seedDeviceCheck(t)
	Chrome(t, 60*time.Second, func(cctx context.Context) {
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken),
			chromedp.Poll(`typeof (window.__gpDeg && window.__gpDeg.DegradationController) === "function"`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("DegradationController seam not exposed: %v", err)
		}
		var res struct {
			ActiveAfter bool   `json:"activeAfter"`
			ReportCount int    `json:"reportCount"`
			LastReport  string `json:"lastReport"`
		}
		js := `(async () => {
			const DC = window.__gpDeg.DegradationController;
			let release;
			const gate = new Promise((r) => { release = r; });
			let active = true; // the sender's encoder-on flag, toggled by setParameters
			const sender = {
				getStats: async () => {
					await gate; // suspend the in-flight sample HERE, mid-pass
					return new Map([
						["o", { type: "outbound-rtp", qualityLimitationReason: "cpu" }],
						["r", { type: "remote-inbound-rtp", roundTripTime: 0.05, fractionLost: 0 }],
					]);
				},
				getParameters: () => ({ encodings: [{ active }] }),
				setParameters: async (p) => { active = p.encodings[0].active; },
			};
			const reports = [];
			const ctrl = new DC({
				getTargets: () => [{ key: "mesh:a", priority: 1, sender }],
				report: (r) => reports.push(r),
			});
			const p = ctrl.sample();   // starts; suspends inside getStats at await gate
			ctrl.recoverNow();         // host override lands mid-sample: re-enable + reset + report null
			release();                 // now let the in-flight getStats resolve with cpu pressure
			await p;                   // the in-flight sample finishes
			const last = reports.length ? reports[reports.length - 1].degraded : null;
			return {
				activeAfter: active,
				reportCount: reports.length,
				lastReport: reports.length ? (last ? last.dir + ":" + last.reason : "null") : "NONE",
			};
		})()`
		if err := chromedp.Run(cctx, chromedp.Evaluate(js, &res,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) },
		)); err != nil {
			t.Fatalf("race repro eval: %v", err)
		}
		// recoverNow re-enabled the sender; the resuming stale sample must NOT re-shed it.
		if !res.ActiveAfter {
			t.Fatalf("recover-now was undone: the in-flight sample re-shed the sender after the override (active=false)")
		}
		// Only recoverNow's degraded:null report should land — the stale pass must not report.
		if res.ReportCount != 1 || res.LastReport != "null" {
			t.Fatalf("the stale in-flight sample reported after recover-now: reportCount=%d lastReport=%q (want 1 / %q)", res.ReportCount, res.LastReport, "null")
		}
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

// T-14 / AC-15: degradation transparency. The host greenroom shows a per-tile degrading/recovering
// badge (driven by the guest's {t:stats} self-report), and a host-only "bump quality now" control
// broadcasts {t:recover-quality}, which forces each publisher to recover immediately — overriding
// the slow recover hysteresis (D-34). A guest sees only its OWN degradation (AC-15, enforced
// server-side in PR-13). Here guest A is forced into cpu degradation, the host sees the badge, and
// "bump quality now" recovers A faster than the unaided hysteresis would.
func TestDegradationTransparency_HostBadgeAndRecoverNow(t *testing.T) {
	s := seedDeviceCheck(t)

	// Guest A: getStats override + enter.
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
		chromedp.Navigate(s.base+"/p/"+s.rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"] .gs-selfview`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest A enter: %v", err)
	}

	// Host: greenroom consumes A → A has a publish sender, and the host sees A's tile.
	hAlloc, cancelHA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelHA()
	hCtx, cancelH := chromedp.NewContext(hAlloc)
	defer cancelH()
	hCtx, cancelHT := context.WithTimeout(hCtx, 150*time.Second)
	defer cancelHT()
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	tileA := `.gr-tile[data-guest="` + s.passID + `"]`
	if err := chromedp.Run(hCtx,
		network.Enable(), setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		chromedp.WaitVisible(tileA, chromedp.ByQuery),
		openPeopleRail(),
	); err != nil {
		t.Fatalf("host greenroom did not render guest A's tile: %v", err)
	}

	// Force A's cpu → A self-reports degradation → the host's tile shows the degrading badge.
	if err := chromedp.Run(aCtx, chromedp.Evaluate(`window.__gpForceLimit = "cpu"`, nil)); err != nil {
		t.Fatalf("force cpu: %v", err)
	}
	if err := chromedp.Run(hCtx, chromedp.WaitVisible(tileA+` .gr-degraded`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the host tile did not show guest A's degrading badge (AC-15): %v", err)
	}

	// Host "bump quality now" → broadcast {t:recover-quality} → guest A's recoverNow() executes (the
	// recovery attempt, D-34). cpu stays forced so this is unambiguous: ONLY recover-quality calls
	// recoverNow (natural recovery never does), so the counter rising proves the whole wiring fired
	// — host button → host WS → server broadcast → A's controller — independent of any timing race.
	if err := chromedp.Run(hCtx, chromedp.Click(`.gr-recover`, chromedp.ByQuery)); err != nil {
		t.Fatalf("host 'bump quality now' click: %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.Poll(`(window.__gpRecoverNowCount || 0) >= 1`,
		nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("'bump quality now' did not reach the guest's recover-now path: %v", err)
	}

	// cpu is still forced, so guest A sheds again and the host tile's degrading badge returns —
	// which is also what re-enables the (degradation-gated) "bump quality now" button. Wait for it
	// before the second click so the button is clickable.
	if err := chromedp.Run(hCtx, chromedp.WaitVisible(tileA+` .gr-degraded`, chromedp.ByQuery)); err != nil {
		t.Fatalf("guest A did not re-degrade after the first bump (cpu still forced): %v", err)
	}
	// And with the pressure cleared, a recover-now restores the guest's own state (degraded clears).
	// The button stays enabled across the clear: A is still shedding (climbing back slowly), so its
	// roster entry keeps a non-null `degraded` until fully recovered.
	if err := chromedp.Run(aCtx, chromedp.Evaluate(`window.__gpForceLimit = null`, nil)); err != nil {
		t.Fatalf("clear cpu: %v", err)
	}
	if err := chromedp.Run(hCtx, chromedp.Click(`.gr-recover`, chromedp.ByQuery)); err != nil {
		t.Fatalf("host 'bump quality now' (2) click: %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.Poll(`document.querySelector('[data-entered]').dataset.degraded === ""`,
		nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("'bump quality now' did not recover guest A's own degradation: %v", err)
	}
}
