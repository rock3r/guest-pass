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

// M5.6 PR-6 AA guard: the greenroom's force-moderation buttons (.gr-force, danger-soft) render their
// label as text on the --bad-soft fill. The lighter chip --bad is only ~3.8:1 there in light (fails
// AA), so they use the darker --danger token; this red-proofs the pair clears 4.5:1 in both themes
// and the OS-dark path. The pair is theme-driven, so it's measured off injected elements (no live
// session needed) against the real greenroom.css in the app bundle.
func TestGreenroom_ForceButtonMeetsAA(t *testing.T) {
	s := seedDeviceCheck(t)

	const contrastJS = `(() => {
		const wrap = document.createElement('div'); wrap.className = 'gr-controls';
		const b = document.createElement('button'); b.className = 'gr-force'; b.textContent = 'Mute';
		wrap.appendChild(b); document.body.appendChild(wrap);
		const cs = getComputedStyle(b); const fg = cs.color, bg = cs.backgroundColor; wrap.remove();
		const toRGB = (c) => c.match(/[\d.]+/g).map(Number);
		const lum = ([r,g,b]) => { const f=(v)=>{v/=255; return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};
			return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b); };
		const la=lum(toRGB(fg)), lb=lum(toRGB(bg)), hi=Math.max(la,lb), lo=Math.min(la,lb); return (hi+0.05)/(lo+0.05);
	})()`

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		_ = chromedp.Run(ctx, network.Enable())
		assertAA := func(mode string, setup ...chromedp.Action) {
			var r float64
			acts := append([]chromedp.Action{}, setup...)
			acts = append(acts, chromedp.Navigate(s.base+"/"), chromedp.WaitVisible(`body`, chromedp.ByQuery), chromedp.Evaluate(contrastJS, &r))
			if err := chromedp.Run(ctx, acts...); err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			if r < 4.5 {
				t.Errorf("%s: .gr-force text-on-fill contrast = %.2f:1, want >= 4.5 (AA)", mode, r)
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
