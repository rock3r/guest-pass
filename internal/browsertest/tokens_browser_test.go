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

// M5.6 AA guard: the status TEXT tokens (--danger/--warn/--good) are used by existing components as
// normal text on the page surface (--bg), so they must clear WCAG AA (4.5:1) in BOTH themes AND in
// the OS-preference dark path (no cookie) — the branch that silently regressed when --danger wasn't
// set there (codex). Contrast is computed in-page with the WCAG formula against the live tokens.
func TestTokens_StatusTextMeetsAA(t *testing.T) {
	s := seedDeviceCheck(t)

	// Returns [danger, warn, good] contrast ratios of each status token vs --bg, read live.
	const contrastJS = `(() => {
		const root = getComputedStyle(document.documentElement);
		const toRGB = (c) => { const d=document.createElement('div'); d.style.color=c; document.body.appendChild(d);
			const m=getComputedStyle(d).color.match(/[\d.]+/g).map(Number); d.remove(); return m; };
		const lum = ([r,g,b]) => { const f=(v)=>{v/=255; return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};
			return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b); };
		const ratio = (a,b) => { const la=lum(toRGB(a)),lb=lum(toRGB(b)),hi=Math.max(la,lb),lo=Math.min(la,lb); return (hi+0.05)/(lo+0.05); };
		const bg = root.getPropertyValue('--bg');
		return ['--danger','--warn','--good'].map(t => ratio(root.getPropertyValue(t), bg));
	})()`

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		_ = chromedp.Run(ctx, network.Enable())

		assertAA := func(mode string, setup ...chromedp.Action) {
			var ratios []float64
			acts := append([]chromedp.Action{}, setup...)
			acts = append(acts,
				chromedp.Navigate(s.base+"/"),
				chromedp.WaitVisible(`body`, chromedp.ByQuery),
				chromedp.Evaluate(contrastJS, &ratios),
			)
			if err := chromedp.Run(ctx, acts...); err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			names := []string{"--danger", "--warn", "--good"}
			for i, r := range ratios {
				if r < 4.5 {
					t.Errorf("%s: %s contrast vs --bg = %.2f:1, want >= 4.5 (AA normal text)", mode, names[i], r)
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
		assertAA("OS dark (no cookie)", setCookie(""), darkMedia) // the branch that regressed
	})
}
