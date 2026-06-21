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

// M5.6 PR-5 AA guard: the admin Hosts table's "(you)" self-marker is 12px real text — it must clear
// WCAG AA against its row background in both themes. The design's faint --ink-3 is only ~3.1:1 there
// (codex); this red-proofs that the product uses an AA-safe token.
func TestAdminConsole_SelfMarkerMeetsAA(t *testing.T) {
	s := seedHostApp(t)
	if err := s.store.SetHostAdmin(context.Background(), s.hostID, true); err != nil {
		t.Fatalf("promote: %v", err)
	}

	const contrastJS = `(() => {
		const el = document.querySelector('.admin-you'); if (!el) return -1;
		const toRGB = (c) => c.match(/[\d.]+/g).map(Number);
		const lum = ([r,g,b]) => { const f=(v)=>{v/=255; return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};
			return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b); };
		const bgOf = (n) => { while(n){ const c=getComputedStyle(n).backgroundColor; if(c && c!=='rgba(0, 0, 0, 0)' && c!=='transparent') return c; n=n.parentElement; } return getComputedStyle(document.body).backgroundColor; };
		const fg=lum(toRGB(getComputedStyle(el).color)), bg=lum(toRGB(bgOf(el))), hi=Math.max(fg,bg),lo=Math.min(fg,bg); return (hi+0.05)/(lo+0.05);
	})()`

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		_ = chromedp.Run(ctx, network.Enable())
		setHost := chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
		})
		assertAA := func(mode string, setup ...chromedp.Action) {
			var r float64
			acts := append([]chromedp.Action{setHost}, setup...)
			acts = append(acts, chromedp.Navigate(s.base+"/admin"), chromedp.WaitVisible(`.admin-you`, chromedp.ByQuery), chromedp.Evaluate(contrastJS, &r))
			if err := chromedp.Run(ctx, acts...); err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			if r < 0 {
				t.Errorf("%s: .admin-you not found", mode)
			} else if r < 4.5 {
				t.Errorf("%s: .admin-you contrast = %.2f:1, want >= 4.5 (AA)", mode, r)
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
