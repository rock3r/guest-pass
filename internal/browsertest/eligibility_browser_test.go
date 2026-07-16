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

// T-9 / AC-9 (EN-23): screenshare eligibility, live. The host grants/revokes can_screen from the
// greenroom People control; the guest's share affordance reflects it live, and a REVOKE runs the
// force-no-share path (the guest sees the "screen share stopped by host" lock notice).
func TestEligibility_HostGrantsAndRevokesLive(t *testing.T) {
	s := seedDeviceCheck(t)

	// Guest A enters (its guest-session view). seedDeviceCheck's pass is not screenshare-eligible, so
	// no share action is available; the surface explains how to request permission instead of silently
	// omitting the capability.
	aCtx := enterGuestSession(t, s.base, s.rawToken, "A")
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`.gs-screen[data-eligible="0"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.gs-screen-toggle`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the unavailable share state should explain the missing permission before the host grants eligibility: %v", err)
	}

	// Host greenroom: guest A's tile carries the (unchecked) eligibility toggle.
	hAlloc, cancelHA := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelHA()
	hCtx, cancelH := chromedp.NewContext(hAlloc)
	defer cancelH()
	hCtx, cancelHT := context.WithTimeout(hCtx, 150*time.Second)
	defer cancelHT()
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	personA := `.gr-person[data-guest="` + s.passID + `"]`
	detailA := `.gr-person-detail[data-guest="` + s.passID + `"]`
	toggle := detailA + ` .gr-screenelig-input`
	if err := chromedp.Run(hCtx,
		network.Enable(), setHostCookie,
		chromedp.Navigate(s.base+"/greenroom"),
		openPeopleRail(),
		chromedp.WaitVisible(personA, chromedp.ByQuery),
		chromedp.Click(personA, chromedp.ByQuery),
		chromedp.WaitVisible(toggle, chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('`+detailA+` .gr-screenelig-input').checked`, nil, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("host greenroom did not render guest A's eligibility toggle (unchecked): %v", err)
	}

	// GRANT: host checks the box → guest A's share affordance appears, and the toggle reflects it.
	if err := chromedp.Run(hCtx, chromedp.Click(toggle, chromedp.ByQuery)); err != nil {
		t.Fatalf("grant click: %v", err)
	}
	if err := chromedp.Run(aCtx, chromedp.WaitVisible(`.gs-screen[data-eligible="1"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the guest's share affordance did not appear after the host granted eligibility: %v", err)
	}
	if err := chromedp.Run(hCtx, chromedp.Poll(`document.querySelector('`+detailA+` .gr-screenelig-input').checked`, nil, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		t.Fatalf("the host's eligibility toggle did not reflect the grant: %v", err)
	}

	// REVOKE: host unchecks → the available action becomes an explicit unavailable state, and the
	// force-no-share lock notice shows.
	if err := chromedp.Run(hCtx, chromedp.Click(toggle, chromedp.ByQuery)); err != nil {
		t.Fatalf("revoke click: %v", err)
	}
	var lock string
	if err := chromedp.Run(aCtx,
		chromedp.WaitVisible(`.gs-screen[data-eligible="0"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.gs-screen-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`.gs-lock`, chromedp.ByQuery),
		chromedp.Text(`.gs-lock`, &lock, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("revoke did not show the unavailable state + force-no-share notice: %v", err)
	}
	if !strings.Contains(lock, "Screen share stopped by host") {
		t.Fatalf("revoke force-lock notice = %q, want 'Screen share stopped by host'", lock)
	}
}
