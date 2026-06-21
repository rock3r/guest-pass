//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// M5.6/AC-1 (T-1+T-2): the three self-hosted design fonts are SERVED (same-origin under /static —
// CSP font-src 'self') and APPLY — headings render in Newsreader, body in Schibsted Grotesk,
// replacing the browser-default serif. `document.fonts.load` forces each woff2 to download (it would
// reject on a 404), so a true result proves all three faces are actually served + loadable, not just
// declared. Proven on a public page (no auth). A font only auto-loads when used, hence the force-load.
func TestFonts_DesignFacesLoadAndApply(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var h1Font, bodyFont string
		var allLoaded bool
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/"),
			chromedp.WaitVisible(`h1`, chromedp.ByQuery),
			chromedp.Evaluate(`getComputedStyle(document.querySelector('h1')).fontFamily`, &h1Font),
			chromedp.Evaluate(`getComputedStyle(document.body).fontFamily`, &bodyFont),
			// Force every self-hosted face to download, then confirm each resolved (the woff2 served).
			chromedp.Evaluate(`Promise.all([
				document.fonts.load('1em "Newsreader"'),
				document.fonts.load('1em "Schibsted Grotesk"'),
				document.fonts.load('1em "Spline Sans Mono"'),
			]).then(() =>
				document.fonts.check('1em "Newsreader"') &&
				document.fonts.check('1em "Schibsted Grotesk"') &&
				document.fonts.check('1em "Spline Sans Mono"')
			).catch(() => false)`, &allLoaded,
				func(ep *runtime.EvaluateParams) *runtime.EvaluateParams { return ep.WithAwaitPromise(true) }),
		); err != nil {
			t.Fatalf("font load/apply: %v", err)
		}
		if !strings.Contains(h1Font, "Newsreader") {
			t.Fatalf("h1 font-family = %q, want the Newsreader heading serif", h1Font)
		}
		if !strings.Contains(bodyFont, "Schibsted Grotesk") {
			t.Fatalf("body font-family = %q, want Schibsted Grotesk", bodyFont)
		}
		if !allLoaded {
			t.Fatal("a self-hosted face failed to load — its woff2 is not being served under /static")
		}
	})
}
