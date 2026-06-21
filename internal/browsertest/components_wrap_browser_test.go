//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// M5.6 PR-2 layout guard: a long-label .btn (e.g. the sources-tab "Regenerate all URLs (my URLs
// leaked)" destructive action) must wrap inside a narrow content column, not overflow it. The
// design's global white-space:nowrap would push the button wider than its container on a phone-width
// viewport (codex). Red-proof: with nowrap the button's rendered width exceeds the 280px column.
func TestComponents_LongButtonWrapsInNarrowContainer(t *testing.T) {
	s := seedDeviceCheck(t)

	const probeJS = `(() => {
		const box = document.createElement('div');
		box.style.width = '280px'; // ~the .app-main content column on a 320px viewport
		const b = document.createElement('button');
		b.className = 'btn btn-danger';
		b.textContent = 'Regenerate all URLs (my URLs leaked)';
		box.appendChild(b); document.body.appendChild(box);
		const bw = b.getBoundingClientRect().width, boxw = box.clientWidth;
		box.remove();
		return { btnWidth: bw, boxWidth: boxw };
	})()`

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var res struct {
			BtnWidth float64 `json:"btnWidth"`
			BoxWidth float64 `json:"boxWidth"`
		}
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/"),
			chromedp.WaitVisible(`body`, chromedp.ByQuery),
			chromedp.Evaluate(probeJS, &res),
		); err != nil {
			t.Fatalf("probe: %v", err)
		}
		if res.BtnWidth > res.BoxWidth+0.5 {
			t.Errorf("long .btn rendered %.0fpx wide in a %.0fpx column — it overflows instead of wrapping", res.BtnWidth, res.BoxWidth)
		}
	})
}
