//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// M5.6 PR-4 AA guard: the host-shell text the v2 restyle introduced — the sidebar nav items, the
// ledger table column headers, and inactive tabs — must clear WCAG AA against their backgrounds in
// both themes. The design uses the faint --ink-3 for column headers (~2.8:1 on the light surface,
// fails AA); this asserts the product bumped them to a readable token. Computed live, in-page WCAG.
func TestHostApp_TextMeetsAA(t *testing.T) {
	s := seedDeviceCheck(t)

	// Each probe returns the contrast of an element's text vs its OWN background (walking up for the
	// effective background when transparent). Measures: a sidebar nav item, a table header, a tab.
	const contrastJS = `(() => {
		const toRGB = (c) => c.match(/[\d.]+/g).map(Number);
		const lum = ([r,g,b]) => { const f=(v)=>{v/=255; return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};
			return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b); };
		const bgOf = (el) => { let n=el; while(n){ const c=getComputedStyle(n).backgroundColor; if(c && c!=='rgba(0, 0, 0, 0)' && c!=='transparent') return c; n=n.parentElement; } return getComputedStyle(document.body).backgroundColor; };
		const ratio = (sel) => { const el=document.querySelector(sel); if(!el) return -1;
			const fg=lum(toRGB(getComputedStyle(el).color)), bg=lum(toRGB(bgOf(el))), hi=Math.max(fg,bg),lo=Math.min(fg,bg); return (hi+0.05)/(lo+0.05); };
		return [ratio('.app-links .navitem:not([aria-current])'), ratio('.invite-table th'), ratio('.tab:not(.tab-active)')];
	})()`

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		_ = chromedp.Run(ctx, network.Enable())
		setHost := chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(auth.SessionCookie, s.hostCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
		})

		assertAA := func(mode string, setup ...chromedp.Action) {
			var ratios []float64
			acts := append([]chromedp.Action{setHost}, setup...)
			acts = append(acts,
				chromedp.Navigate(s.base+"/app/streams/"+s.streamID),
				chromedp.WaitVisible(`.invite-table th`, chromedp.ByQuery),
				chromedp.Evaluate(contrastJS, &ratios),
			)
			if err := chromedp.Run(ctx, acts...); err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			names := []string{"sidebar nav item", "table header", "inactive tab"}
			for i, r := range ratios {
				if r < 0 {
					t.Errorf("%s: %s not found", mode, names[i])
					continue
				}
				if r < 4.5 {
					t.Errorf("%s: %s contrast = %.2f:1, want >= 4.5 (AA)", mode, names[i], r)
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
