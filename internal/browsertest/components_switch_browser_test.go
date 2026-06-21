//go:build browser

package browsertest

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// M5.6 PR-2 a11y guard: the .switch toggle hides its native checkbox visually but MUST keep it in
// the tab order / accessibility tree (not display:none), so keyboard + screen-reader users can
// focus and toggle it and .switch:focus-within fires (codex). Red-proof: with display:none the
// input cannot take focus (activeElement stays the body) and the focus-within outline never shows.
func TestComponents_SwitchIsKeyboardFocusable(t *testing.T) {
	s := seedDeviceCheck(t)

	const probeJS = `(() => {
		const l = document.createElement('label'); l.className = 'switch';
		const i = document.createElement('input'); i.type = 'checkbox';
		const tr = document.createElement('span'); tr.className = 'track';
		const th = document.createElement('span'); th.className = 'thumb';
		l.append(i, tr, th); document.body.appendChild(l);
		i.focus();
		const focused = document.activeElement === i;
		const outline = getComputedStyle(l).outlineStyle; // .switch:focus-within → solid
		l.remove();
		return { focused, outline };
	})()`

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var res struct {
			Focused bool   `json:"focused"`
			Outline string `json:"outline"`
		}
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/"),
			chromedp.WaitVisible(`body`, chromedp.ByQuery),
			chromedp.Evaluate(probeJS, &res),
		); err != nil {
			t.Fatalf("probe: %v", err)
		}
		if !res.Focused {
			t.Errorf(".switch input could not take keyboard focus (activeElement != input) — it must not be display:none")
		}
		if res.Outline == "none" || res.Outline == "" {
			t.Errorf(".switch:focus-within outline did not fire (outline-style=%q) — the focused input is not in the control", res.Outline)
		}
	})
}
