//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
)

// AC-4 / T-4: a host greenroom WS drop AUTO-RECONNECTS — it shows the recoverable "reconnecting"
// banner (not an error screen) and recovers to live while the host's room persists server-side
// (D-40). No guests needed: the host's own signaling socket is force-closed and the island
// re-establishes it. This is the recovery half of the terminate taxonomy (the terminal session-
// ended half is TestGreenroom_ForceEndShowsEndedScreen).
func TestGreenroom_TransientDropAutoReconnects(t *testing.T) {
	s := seedHostApp(t)

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	injectRecorder := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(wsRecorderJS).Do(ctx)
		return err
	})

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		if err := chromedp.Run(ctx,
			network.Enable(),
			injectRecorder,
			setHostCookie,
			chromedp.Navigate(s.base+"/greenroom"),
			chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("host greenroom did not go live: %v", err)
		}

		// Force-close the greenroom's live signaling socket — a transient drop.
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__gpCloseLastWS()`, nil)); err != nil {
			t.Fatalf("force-close greenroom socket: %v", err)
		}
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(`.greenroom[data-state="reconnecting"] .gr-reconnecting`, chromedp.ByQuery), // shows the banner…
			chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),                          // …then auto-recovers
		); err != nil {
			t.Fatalf("greenroom did not auto-reconnect after a dropped socket (AC-4): %v", err)
		}

		// A transient drop must never route to the terminal ended/error screens.
		var terminal bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`!!document.querySelector('.gr-ended') || !!document.querySelector('.gr-error')`, &terminal,
		)); err != nil {
			t.Fatalf("probe ended/error: %v", err)
		}
		if terminal {
			t.Fatal("a transient greenroom drop must not show the ended/error screen")
		}
	})
}

// AC-4 / EN-16 (codex): two greenroom tabs for the SAME host must NOT reconnect-war. The newer tab
// displaces the older via one-connection-per-identity; the older receives the TERMINAL
// terminate:displaced and stops on an "opened in another tab" screen (it does NOT auto-reconnect and
// evict the newer one), while the newer tab stays live.
func TestGreenroom_SecondTabDisplacesFirstWithoutWar(t *testing.T) {
	s := seedHostApp(t)
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	open := func(ctx context.Context) error {
		return chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/greenroom"),
			chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),
		)
	}

	// Use the root target as tab A, then create tab B after tab A has allocated the browser.
	// That produces two sibling targets in one Chrome process; deriving both children before the
	// root has a Browser causes chromedp to allocate independent browser processes instead.
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	tabA, cancelA := chromedp.NewContext(allocCtx)
	defer cancelA()
	tabA, cancelDeadline := context.WithTimeout(tabA, 90*time.Second)
	defer cancelDeadline()

	if err := open(tabA); err != nil {
		t.Fatalf("tab A greenroom did not go live: %v", err)
	}
	// Open tab B from tab A itself and attach to the resulting target. This is the browser's actual
	// "open in a new tab" path: the two targets share Chrome and its cookie jar, so the second
	// greenroom connection faithfully displaces the first one.
	newTab := chromedp.WaitNewTarget(tabA, func(info *target.Info) bool {
		return info.URL == s.base+"/greenroom"
	})
	if err := chromedp.Run(tabA, chromedp.Evaluate(`window.open("/greenroom", "_blank"); void 0`, nil)); err != nil {
		t.Fatalf("open tab B: %v", err)
	}
	tabB, cancelB := chromedp.NewContext(tabA, chromedp.WithTargetID(<-newTab))
	defer cancelB()
	if err := chromedp.Run(tabB, chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("tab B greenroom did not go live: %v", err)
	}

	// Tab A must route to the terminal "displaced" screen and STAY there (no reconnect war).
	if err := chromedp.Run(tabA,
		chromedp.WaitVisible(`.greenroom[data-state="displaced"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("displaced tab A did not show the 'opened in another tab' screen: %v", err)
	}
	var bodyText string
	if err := chromedp.Run(tabA, chromedp.Text(`.greenroom[data-state="displaced"] .gr-error`, &bodyText, chromedp.ByQuery)); err != nil {
		t.Fatalf("displaced banner missing: %v", err)
	}
	if !strings.Contains(strings.ToLower(bodyText), "another tab") {
		t.Fatalf("displaced banner = %q, want an 'another tab' message", bodyText)
	}
	// It must NOT recover to live (which would mean it warred back and evicted tab B).
	time.Sleep(2 * time.Second)
	var stillDisplaced bool
	if err := chromedp.Run(tabA, chromedp.Evaluate(`!!document.querySelector('.greenroom[data-state="displaced"]')`, &stillDisplaced)); err != nil {
		t.Fatalf("re-check tab A: %v", err)
	}
	if !stillDisplaced {
		t.Fatal("displaced tab A reconnected — it must not war with the active tab")
	}
	// Tab B stays live throughout.
	if err := chromedp.Run(tabB, chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the active tab B did not stay live: %v", err)
	}
}

// M5.5 / D-27: when an admin force-ends a host's session (the suspend cascade → TerminateHostRoom),
// every peer — INCLUDING the host's own greenroom — gets the terminal session-ended reason. The
// greenroom must surface a clear "this session has ended" screen, not just silently drop to an
// empty grid (the gap found in the M5 gate-2 smoke). No guests/media needed: a lone host connects,
// then the room is terminated out from under it.
func TestGreenroom_ForceEndShowsEndedScreen(t *testing.T) {
	s := seedHostApp(t)

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		// The host opens the greenroom; its signaling socket comes up (data-state="live").
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/greenroom"),
			chromedp.WaitVisible(`.greenroom[data-state="live"]`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("host greenroom did not go live: %v", err)
		}

		// Force-end the host's room. Retry to close the tiny race between the client onopen
		// (data-state="live") and the server registering the host peer in the room — an early call
		// is a harmless no-op (no room yet); once the host is registered the terminate lands.
		deadline := time.Now().Add(30 * time.Second)
		for {
			s.hub.TerminateHostRoom(s.hostID, signaling.TerminateSessionEnded)
			var present bool
			if err := chromedp.Run(ctx, chromedp.Evaluate(`!!document.querySelector('.gr-ended')`, &present)); err != nil {
				t.Fatalf("probe for ended screen: %v", err)
			}
			if present {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("greenroom never showed the ended screen after force-end")
			}
			time.Sleep(250 * time.Millisecond)
		}

		// The selector itself proves data-state flipped to "ended" AND the banner rendered.
		var endedText string
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(`.greenroom[data-state="ended"] .gr-ended`, chromedp.ByQuery),
			chromedp.Text(`.gr-ended`, &endedText, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("ended screen did not render: %v", err)
		}
		if !strings.Contains(strings.ToLower(endedText), "ended") {
			t.Fatalf("ended screen text = %q, want a session-ended message", endedText)
		}
	})
}
