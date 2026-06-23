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
	"github.com/rock3r/guest-pass/internal/store"
)

// M5.5/D-9: the no-JS footer toggle cycles System → Light → Dark by POSTing /theme (PRG), the choice
// is stamped onto <html data-theme> by the SERVER (no FOUC — it's in the initial HTML, not applied by
// JS), and it persists across navigations via the gp_theme cookie. It actually themes the surfaces:
// the public <body> background flips (P2), and a host card's background flips to the dark surface
// rather than staying a hard-coded white (P1) — both regressions caught by codex.
func TestTheme_TogglePersistsNoFOUC(t *testing.T) {
	s := seedHostApp(t)
	// A stream so the dashboard renders a .stream-tile-card surface to check in dark mode (P1).
	if _, err := s.store.CreateStream(context.Background(), store.CreateStreamParams{HostID: s.hostID, Title: "Themed Show"}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var initial, paperDark, bodyBgDark string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/"),
			chromedp.WaitVisible(`.theme-icon-btn`, chromedp.ByQuery),
			// No cookie yet → no explicit data-theme (the page follows the OS).
			chromedp.Evaluate(`document.documentElement.getAttribute('data-theme') || ""`, &initial),
			// Cycle System → Light. The toggle submits → PRG → the page reloads; waiting on the NEW
			// page's <html data-theme="…"> attribute selector is the deterministic signal (it can't
			// match the old page, so it waits out the navigation — unlike a bare button WaitVisible).
			chromedp.Click(`.theme-icon-btn`, chromedp.ByQuery),
			chromedp.WaitVisible(`html[data-theme="light"]`, chromedp.ByQuery),
			// Cycle Light → Dark.
			chromedp.Click(`.theme-icon-btn`, chromedp.ByQuery),
			chromedp.WaitVisible(`html[data-theme="dark"]`, chromedp.ByQuery),
			chromedp.Evaluate(`getComputedStyle(document.documentElement).getPropertyValue('--paper').trim()`, &paperDark),
			// P2: the public <body> actually consumes the token — its painted background is the dark
			// surface (rgb 21,24,15 = #15180f), not the browser default white.
			chromedp.Evaluate(`getComputedStyle(document.body).backgroundColor`, &bodyBgDark),
		); err != nil {
			t.Fatalf("theme toggle flow: %v", err)
		}
		if initial != "" {
			t.Fatalf("no cookie should mean no explicit data-theme, got %q", initial)
		}
		if !strings.Contains(paperDark, "15180f") {
			t.Fatalf("dark --paper = %q, want the dark surface (#15180f)", paperDark)
		}
		if !strings.Contains(bodyBgDark, "21, 24, 15") {
			t.Fatalf("public <body> background in dark = %q, want the dark paper rgb(21, 24, 15)", bodyBgDark)
		}

		// No-FOUC: a fresh navigation already has data-theme=dark in the INITIAL server HTML — the
		// theme is decided server-side from the cookie, not flipped by JS after load.
		var navTheme string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/signin"),
			chromedp.Evaluate(`document.documentElement.getAttribute('data-theme') || ""`, &navTheme),
		); err != nil {
			t.Fatalf("re-navigation: %v", err)
		}
		if navTheme != "dark" {
			t.Fatalf("after persisting dark, a fresh page = %q, want dark (no-FOUC server stamp)", navTheme)
		}

		// P1: a host surface (the dashboard stream card) flips to the dark surface token rather than
		// staying a hard-coded white that would leave light text unreadable. The gp_theme=dark cookie
		// persists from the toggle above; set the host session cookie and load the dashboard.
		var cardBgDark string
		if err := chromedp.Run(ctx,
			setHostCookie,
			chromedp.Navigate(s.base+"/app"),
			chromedp.WaitVisible(`.stream-tile-card`, chromedp.ByQuery),
			chromedp.Evaluate(`getComputedStyle(document.querySelector('.stream-tile-card')).backgroundColor`, &cardBgDark),
		); err != nil {
			t.Fatalf("host dashboard in dark: %v", err)
		}
		if !strings.Contains(cardBgDark, "29, 33, 21") {
			t.Fatalf("host .stream-tile-card background in dark = %q, want the dark surface rgb(29, 33, 21) — not white", cardBgDark)
		}
	})
}
