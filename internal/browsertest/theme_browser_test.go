//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// M5.5/D-9: the no-JS footer toggle cycles System → Light → Dark by POSTing /theme (PRG), the choice
// is stamped onto <html data-theme> by the SERVER (no FOUC — it's in the initial HTML, not applied by
// JS), and it persists across navigations via the gp_theme cookie. Dark actually flips the --paper
// surface token.
func TestTheme_TogglePersistsNoFOUC(t *testing.T) {
	s := seedHostApp(t) // only used for its running server; the landing + toggle are public

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var initial, paperDark string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/"),
			chromedp.WaitVisible(`.theme-btn`, chromedp.ByQuery),
			// No cookie yet → no explicit data-theme (the page follows the OS).
			chromedp.Evaluate(`document.documentElement.getAttribute('data-theme') || ""`, &initial),
			// Cycle System → Light. The toggle submits → PRG → the page reloads; waiting on the NEW
			// page's <html data-theme="…"> attribute selector is the deterministic signal (it can't
			// match the old page, so it waits out the navigation — unlike a bare button WaitVisible).
			chromedp.Click(`.theme-btn`, chromedp.ByQuery),
			chromedp.WaitVisible(`html[data-theme="light"]`, chromedp.ByQuery),
			// Cycle Light → Dark.
			chromedp.Click(`.theme-btn`, chromedp.ByQuery),
			chromedp.WaitVisible(`html[data-theme="dark"]`, chromedp.ByQuery),
			chromedp.Evaluate(`getComputedStyle(document.documentElement).getPropertyValue('--paper').trim()`, &paperDark),
		); err != nil {
			t.Fatalf("theme toggle flow: %v", err)
		}
		if initial != "" {
			t.Fatalf("no cookie should mean no explicit data-theme, got %q", initial)
		}
		if !strings.Contains(paperDark, "15180f") {
			t.Fatalf("dark --paper = %q, want the dark surface (#15180f)", paperDark)
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
	})
}
