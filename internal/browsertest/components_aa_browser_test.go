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

// M5.6 PR-2 AA guard for the FILLED danger button (.btn-danger): white text on the danger fill must
// clear WCAG AA (4.5:1, it's 14.5px bold = normal text, not "large") in BOTH themes AND the OS-dark
// path. The design's dark --bad (#f0654f) is a chip fill, not a button fill — white on it is only
// ~3.15:1 (codex). The --danger-fill token carries an AA-clean value per theme; this red-proofs it.
func TestComponents_DangerButtonMeetsAA(t *testing.T) {
	s := seedDeviceCheck(t)

	// Mounts a real <button class="btn btn-danger">, reads its computed text + fill colours, and
	// returns their WCAG contrast ratio (computed in-page with the standard luminance formula).
	const contrastJS = `(() => {
		const b = document.createElement('button');
		b.className = 'btn btn-danger'; b.textContent = 'Delete';
		document.body.appendChild(b);
		const cs = getComputedStyle(b);
		const fg = cs.color, bg = cs.backgroundColor;
		b.remove();
		const toRGB = (c) => c.match(/[\d.]+/g).map(Number);
		const lum = ([r,g,b]) => { const f=(v)=>{v/=255; return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};
			return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b); };
		const la = lum(toRGB(fg)), lb = lum(toRGB(bg)), hi = Math.max(la,lb), lo = Math.min(la,lb);
		return (hi+0.05)/(lo+0.05);
	})()`

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		_ = chromedp.Run(ctx, network.Enable())

		assertAA := func(mode string, setup ...chromedp.Action) {
			var ratio float64
			acts := append([]chromedp.Action{}, setup...)
			acts = append(acts,
				chromedp.Navigate(s.base+"/"),
				chromedp.WaitVisible(`body`, chromedp.ByQuery),
				chromedp.Evaluate(contrastJS, &ratio),
			)
			if err := chromedp.Run(ctx, acts...); err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			if ratio < 4.5 {
				t.Errorf("%s: .btn-danger white-on-fill contrast = %.2f:1, want >= 4.5 (AA normal text)", mode, ratio)
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
