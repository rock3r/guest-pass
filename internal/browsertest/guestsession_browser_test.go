//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// enterGuestSession opens a FRESH fake-media browser, runs the device check, enters, and waits for
// the in-session guest-session surface to render. Each fake-media guest needs its OWN browser (own
// synthetic camera) — two publishers can't share one fake device (M2 learning). The returned ctx
// stays alive (cancels deferred to test end) so the guest keeps publishing + receiving.
func enterGuestSession(t *testing.T, base, rawToken, label string) context.Context {
	t.Helper()
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(cancel)
	ctx, cancelT := context.WithTimeout(ctx, 150*time.Second)
	t.Cleanup(cancelT)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(base+"/p/"+rawToken),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"] .gs-selfview`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest %s enter: %v", label, err)
	}
	return ctx
}

// T-11 / AC-12: the guest's in-session island. After entering, the guest sees a self-view, the
// three-state on-air SELF pill (D-24), a backstage chat with the "not recorded — off the record"
// microcopy (EN-20), a raise-hand control, and a separate "muted/hidden by host" force-lock notice
// (D-13). This proves: the self-view renders live frames + the on-air pill renders; a backstage
// message ROUND-TRIPS the server relay to a SECOND guest (the panels render only relayed {t:chat}
// frames — never an optimistic echo — so the message reaching the OTHER guest proves the relay;
// "leaves no trace" is the server-side EN-20 invariant proven by the merged PR-6 tests); raise-hand
// surfaces on the host greenroom tile; and a host force-mute shows the live force-lock notice on the
// guest. Everyone-backstage thumbnails (the guest↔guest mesh, D-10) are PR-11b.
func TestGuestSession_ChatHandOnAirLock(t *testing.T) {
	s := seedDeviceCheck(t)

	// Two guests, each in its own fake-media browser, both in-session.
	aCtx := enterGuestSession(t, s.base, s.rawToken, "A")
	bCtx := enterGuestSession(t, s.base, s.rawTokenB, "B")

	// Guest A's surface: self-view shows live frames, the on-air SELF pill renders, and the chat
	// carries the EN-20 "off the record" microcopy.
	if err := chromedp.Run(aCtx,
		chromedp.Poll(`document.querySelector('.gs-selfview').videoWidth > 0`, nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.WaitVisible(`.dc-onair[data-onair]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-chat`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest-session surface (self-view / on-air pill / chat) did not render: %v", err)
	}
	var note string
	if err := chromedp.Run(aCtx, chromedp.Text(`.gs-chat-note`, &note, chromedp.ByQuery)); err != nil {
		t.Fatalf("chat privacy microcopy missing: %v", err)
	}
	if n := strings.ToLower(note); !strings.Contains(n, "not recorded") || !strings.Contains(n, "off the record") {
		t.Fatalf("chat microcopy = %q, want it to mention 'not recorded' and 'off the record' (EN-20)", note)
	}

	// Backstage chat round-trips: A sends a message and it reaches GUEST B over the relay (and shows
	// in A's own log — both panels render only relayed frames, so receipt proves the server round-trip).
	// A unique sentinel (not a natural-language phrase) so the substring match can't collide with any
	// other chrome text now or if this helper is reused with another message later.
	const msg = "backstage-roundtrip-7Qx9z"
	const seen = `[...document.querySelectorAll('.gs-chat-msg')].some((m) => m.textContent.includes('backstage-roundtrip-7Qx9z'))`
	if err := chromedp.Run(aCtx,
		chromedp.SendKeys(`.gs-chat-input`, msg, chromedp.ByQuery),
		chromedp.Click(`.gs-chat-send`, chromedp.ByQuery),
		chromedp.Poll(seen, nil, chromedp.WithPollingTimeout(15*time.Second)),
	); err != nil {
		t.Fatalf("backstage chat did not appear in the sender's log: %v", err)
	}
	if err := chromedp.Run(bCtx, chromedp.Poll(seen, nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("backstage chat did not round-trip the relay to the other guest: %v", err)
	}

	// Host: greenroom grid. Target guest A's tile by its stable peer id (== pass id) so the two
	// guest tiles don't race the selector.
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
	); err != nil {
		t.Fatalf("host greenroom did not render guest A's tile: %v", err)
	}

	// Raise hand on guest A → it surfaces on A's host tile (the roster carries handRaised, PR-7).
	if err := chromedp.Run(aCtx, chromedp.Click(`.gs-hand`, chromedp.ByQuery)); err != nil {
		t.Fatalf("raise-hand click: %v", err)
	}
	if err := chromedp.Run(hCtx, chromedp.WaitVisible(tileA+` .gr-hand`, chromedp.ByQuery)); err != nil {
		t.Fatalf("raised hand did not surface on guest A's host tile: %v", err)
	}

	// Host force-mutes guest A → A's guest-session shows the separate force-lock notice (D-13).
	if err := chromedp.Run(hCtx,
		chromedp.Click(tileA+` .gr-force[data-kind="mic"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("host force-mute click: %v", err)
	}
	var lock string
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`.gs-lock`, chromedp.ByQuery),
		chromedp.Text(`.gs-lock`, &lock, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest-session did not show the force-lock notice: %v", err)
	}
	if !strings.Contains(lock, "Muted by host") {
		t.Fatalf("force-lock notice = %q, want it to contain 'Muted by host'", lock)
	}
}
