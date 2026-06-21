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

// M5.6 PR-7 AA guard for the guest islands: the new readable text the device-check + in-session
// restyle introduced must clear WCAG AA in both themes and the OS-dark path — the on-air banner
// (white on --live-fill), the screenshare status line (--good), and the mono device labels (--ink-2),
// each against its effective background. Measured off injected elements against the real island CSS.
func TestGuest_TextMeetsAA(t *testing.T) {
	s := seedDeviceCheck(t)

	// Builds each probe element, reads its computed text colour vs its effective background (walking
	// up for the first non-transparent bg), and returns the three contrast ratios.
	const contrastJS = `(() => {
		const toRGB = (c) => c.match(/[\d.]+/g).map(Number);
		const lum = ([r,g,b]) => { const f=(v)=>{v/=255; return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};
			return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b); };
		const bgOf = (n) => { while(n){ const c=getComputedStyle(n).backgroundColor; if(c && c!=='rgba(0, 0, 0, 0)' && c!=='transparent') return c; n=n.parentElement; } return getComputedStyle(document.body).backgroundColor; };
		const ratio = (el) => { document.body.appendChild(el); const fg=lum(toRGB(getComputedStyle(el).color)), bg=lum(toRGB(bgOf(el))); el.remove(); const hi=Math.max(fg,bg),lo=Math.min(fg,bg); return (hi+0.05)/(lo+0.05); };
		const onair = document.createElement('p'); onair.className='dc-onair'; onair.setAttribute('data-onair','on-air'); onair.textContent='on air';
		const stat = document.createElement('p'); stat.className='gs-screen-status'; stat.textContent='Backstage';
		const label = document.createElement('span'); label.className='dc-device-label'; label.textContent='Camera';
		return [ratio(onair), ratio(stat), ratio(label)];
	})()`

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		_ = chromedp.Run(ctx, network.Enable())
		assertAA := func(mode string, setup ...chromedp.Action) {
			var ratios []float64
			acts := append([]chromedp.Action{}, setup...)
			acts = append(acts, chromedp.Navigate(s.base+"/"), chromedp.WaitVisible(`body`, chromedp.ByQuery), chromedp.Evaluate(contrastJS, &ratios))
			if err := chromedp.Run(ctx, acts...); err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			names := []string{".dc-onair[on-air]", ".gs-screen-status", ".dc-device-label"}
			for i, r := range ratios {
				if r < 4.5 {
					t.Errorf("%s: %s contrast = %.2f:1, want >= 4.5 (AA)", mode, names[i], r)
				}
			}
		}
		setCookie := func(v string) chromedp.Action {
			return chromedp.ActionFunc(func(ctx context.Context) error { return network.SetCookie("gp_theme", v).WithURL(s.base).Do(ctx) })
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
