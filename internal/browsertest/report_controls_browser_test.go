//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// M5.6 PR-3 readability guard: the report form's <select>/<textarea> must render in the readable body
// type, NOT the mono/uppercase/letter-spaced micro-label styling. The label text wraps the control,
// so if the micro-label styling sits on the <label> it inherits into the control and a reporter's
// typed message comes out as uppercase monospace (codex). Red-proof: assert the control's computed
// text-transform is none and its font is the body face, not the mono face.
func TestReport_ControlsAreReadable(t *testing.T) {
	s := seedDeviceCheck(t)

	const probeJS = `(() => {
		const ta = document.querySelector('.report-form textarea[name="message"]');
		const cs = getComputedStyle(ta);
		return { transform: cs.textTransform, family: cs.fontFamily, spacing: cs.letterSpacing };
	})()`

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var res struct {
			Transform string `json:"transform"`
			Family    string `json:"family"`
			Spacing   string `json:"spacing"`
		}
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/p/"+s.rawToken+"/report"),
			chromedp.WaitVisible(`.report-form`, chromedp.ByQuery),
			chromedp.Evaluate(probeJS, &res),
		); err != nil {
			t.Fatalf("probe: %v", err)
		}
		if res.Transform != "none" {
			t.Errorf("report textarea text-transform = %q, want none (reporter text must not be uppercased)", res.Transform)
		}
		if strings.Contains(res.Family, "Spline Sans Mono") {
			t.Errorf("report textarea font-family = %q, want the body face (not the mono micro-label face)", res.Family)
		}
		if res.Spacing != "normal" && res.Spacing != "0px" {
			t.Errorf("report textarea letter-spacing = %q, want normal (not the micro-label tracking)", res.Spacing)
		}
	})
}
