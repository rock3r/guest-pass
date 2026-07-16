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

// T-10 / AC-11: from a greenroom tile the host force-mutes a guest — the lock is LIVE-VISIBLE on
// the tile, the guest stops its mic AT SOURCE (RF-8), and release restores it; then the host
// promotes the guest, flipping its tile role. The server-side "a forced-off guest's self-state
// can't self-enable" guarantee is proven by the merged PR-3 web test; this exercises the UI.
func TestGreenroom_ModerationControls(t *testing.T) {
	s := seedGrid(t, 1)
	raw := s.rawTokens[0]

	// Guest: its own fake-media browser, publishing (kept alive so the host can consume it).
	gAlloc, cancelGA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelGA()
	gCtx, cancelG := chromedp.NewContext(gAlloc)
	defer cancelG()
	gCtx, cancelGT := context.WithTimeout(gCtx, 150*time.Second)
	defer cancelGT()
	if err := chromedp.Run(gCtx,
		chromedp.Navigate(s.base+"/p/"+raw),
		chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		chromedp.Click(`.dc-start`, chromedp.ByQuery),
		chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.dc-video').videoWidth > 0`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		chromedp.Click(`.dc-enter`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-entered="1"][data-pub="live"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("guest publish: %v", err)
	}

	// Host: greenroom grid renders the guest tile over P2P.
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
		chromedp.WaitVisible(`.gr-tile[data-role="guest"] .gr-video`, chromedp.ByQuery),
		openPeopleRail(),
		chromedp.WaitVisible(`.gr-person[data-guest]`, chromedp.ByQuery),
		chromedp.Click(`.gr-person[data-guest]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("host grid did not render the guest tile: %v", err)
	}
	if err := chromedp.Run(hCtx, chromedp.Poll(
		`document.querySelector('.gr-tile[data-role="guest"] .gr-video').videoWidth > 0`,
		nil, chromedp.WithPollingTimeout(90*time.Second),
	)); err != nil {
		t.Fatalf("host tile did not receive the guest's video: %v", err)
	}

	// Host force-mutes the guest from its selected People-rail controls → the tile shows the live,
	// per-modality lock notice (D-13).
	if err := chromedp.Run(hCtx,
		chromedp.Click(`.gr-person-detail .gr-force[data-kind="mic"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gr-tile[data-role="guest"] .gr-lock`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("force-mute did not show the live lock on the tile: %v", err)
	}
	var lockText string
	if err := chromedp.Run(hCtx, chromedp.Text(`.gr-tile[data-role="guest"] .gr-lock`, &lockText, chromedp.ByQuery)); err != nil {
		t.Fatalf("read lock text: %v", err)
	}
	if !strings.Contains(lockText, "Muted by host") {
		t.Fatalf("lock notice = %q, want it to contain 'Muted by host'", lockText)
	}

	// The guest reacts AT SOURCE (RF-8): its mic modality enters its own locked set.
	if err := chromedp.Run(gCtx, chromedp.Poll(
		`(document.querySelector('[data-entered]').dataset.locked || '').split(',').includes('mic')`,
		nil, chromedp.WithPollingTimeout(15*time.Second),
	)); err != nil {
		t.Fatalf("guest did not stop its mic at source on the lock: %v", err)
	}

	// Host releases → the lock clears on the tile and the guest re-enables its mic.
	if err := chromedp.Run(hCtx,
		chromedp.Click(`.gr-person-detail .gr-release[data-kind="mic"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.gr-tile[data-role="guest"] .gr-lock`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("release did not clear the lock: %v", err)
	}
	if err := chromedp.Run(gCtx, chromedp.Poll(
		`!((document.querySelector('[data-entered]').dataset.locked || '').split(',').includes('mic'))`,
		nil, chromedp.WithPollingTimeout(15*time.Second),
	)); err != nil {
		t.Fatalf("guest mic was not re-enabled after release: %v", err)
	}

	// Host promotes the guest → the tile role flips to co-host (live, D-15).
	if err := chromedp.Run(hCtx,
		chromedp.Click(`.gr-person-detail .gr-role`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gr-tile[data-role="cohost"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("promote did not flip the tile role to co-host: %v", err)
	}
}
