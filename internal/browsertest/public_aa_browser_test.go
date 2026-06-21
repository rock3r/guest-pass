//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// M5.6 PR-3 AA guard: the public surfaces' real text — hero heading + body, the footer license/source
// line — must clear WCAG AA (4.5:1) against the page background in BOTH themes and the OS-dark path.
// The design's faint --ink-3 is only ~2.8:1 on the light --bg, so footer/secondary text uses --ink-2;
// this red-proofs that every styled public-text colour stays readable (computed live, in-page WCAG).
func TestPublic_TextMeetsAA(t *testing.T) {
	s := seedDeviceCheck(t)

	// Returns the contrast ratios (vs document.body's background) of the hero heading, hero body
	// paragraph, and footer text — read live off the rendered landing page.
	const contrastJS = `(() => {
		const toRGB = (c) => c.match(/[\d.]+/g).map(Number);
		const lum = ([r,g,b]) => { const f=(v)=>{v/=255; return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};
			return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b); };
		const bg = lum(toRGB(getComputedStyle(document.body).backgroundColor));
		const ratio = (sel) => { const el = document.querySelector(sel); if (!el) return 21;
			const fg = lum(toRGB(getComputedStyle(el).color)); const hi=Math.max(fg,bg),lo=Math.min(fg,bg); return (hi+0.05)/(lo+0.05); };
		return [ratio('.hero h1'), ratio('.hero p'), ratio('.site-footer p')];
	})()`

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		_ = chromedp.Run(ctx, network.Enable())

		assertAA := func(mode string, setup ...chromedp.Action) {
			var ratios []float64
			acts := append([]chromedp.Action{}, setup...)
			acts = append(acts,
				chromedp.Navigate(s.base+"/"),
				chromedp.WaitVisible(`.hero`, chromedp.ByQuery),
				chromedp.Evaluate(contrastJS, &ratios),
			)
			if err := chromedp.Run(ctx, acts...); err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			names := []string{".hero h1", ".hero p", ".site-footer p"}
			for i, r := range ratios {
				if r < 4.5 {
					t.Errorf("%s: %s contrast vs page bg = %.2f:1, want >= 4.5 (AA normal text)", mode, names[i], r)
				}
			}
		}
		setCookie := func(v string) chromedp.Action {
			return chromedp.ActionFunc(func(ctx context.Context) error {
				return network.SetCookie("gp_theme", v).WithURL(s.base).Do(ctx)
			})
		}
		clearMedia := chromedp.ActionFunc(func(ctx context.Context) error { return emulation.SetEmulatedMedia().Do(ctx) })
		darkMedia := chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "dark"}}).Do(ctx)
		})

		assertAA("explicit light", setCookie("light"), clearMedia)
		assertAA("explicit dark", setCookie("dark"), clearMedia)
		assertAA("OS dark (no cookie)", setCookie(""), darkMedia)
	})
}
