//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// M5.6 PR-3 layout guard: the public surfaces must not scroll horizontally on a phone-width viewport.
// The token layer skips a global box-sizing reset, so the width:100% + padded public containers (and
// the width:100% form controls) would overflow a 360px screen without border-box (codex). Red-proof:
// without border-box, document.scrollWidth exceeds the 360px client width on the report page.
func TestPublic_NoHorizontalOverflowOnPhone(t *testing.T) {
	s := seedDeviceCheck(t)
	pages := []struct{ name, path, wait string }{
		{"landing", "/", `.hero`},
		{"signin", "/signin", `.signin`},
		{"report", "/p/" + s.rawToken + "/report", `.report-form`},
		{"error", "/no-such-page", `.error-screen`},
	}

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(360, 800)); err != nil {
			t.Fatalf("viewport: %v", err)
		}
		for _, p := range pages {
			var overflow int64
			if err := chromedp.Run(ctx,
				chromedp.Navigate(s.base+p.path),
				chromedp.WaitVisible(p.wait, chromedp.ByQuery),
				chromedp.Evaluate(`document.documentElement.scrollWidth - document.documentElement.clientWidth`, &overflow),
			); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			if overflow > 1 { // allow a sub-pixel rounding slack
				t.Errorf("%s overflows a 360px viewport by %dpx (horizontal scroll) — needs border-box", p.name, overflow)
			}
		}
	})
}
