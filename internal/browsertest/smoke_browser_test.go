//go:build browser

package browsertest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// The harness builds the REAL app bundle, serves it, and drives it in headless Chrome:
// the SPIKE-1 island mounts (vendored Preact + hooks) and its useState counter increments
// on a click. This proves the esbuild→serve→chromedp toolchain end to end (T-5 / [BROWSER]).
func TestSmoke_AppIslandMountsAndIncrements(t *testing.T) {
	h := New(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/", Page(`<!doctype html><html><head><meta charset="utf-8"></head>`+
			`<body><div id="app"></div><script type="module" src="/static/app.js"></script></body></html>`))
	})

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		var text string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(h.URL),
			chromedp.WaitVisible(`.gp-probe`, chromedp.ByQuery),
			chromedp.Click(`.gp-probe`, chromedp.ByQuery),
			chromedp.Text(`.gp-probe`, &text, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("chromedp run: %v", err)
		}
		if !strings.Contains(text, "clicked 1") {
			t.Fatalf("island counter text = %q, want it to show 'clicked 1 time'", text)
		}
	})
}

// The fake-media flags supply a synthetic camera + mic so getUserMedia resolves with real
// tracks and no hardware. This guards the foundation the device-check / PeerLink browser
// tests (PR-6+) build on.
func TestSmoke_FakeMediaProvidesTracks(t *testing.T) {
	h := New(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/", Page(`<!doctype html><html><head><meta charset="utf-8"></head><body>
<script>
window.__media = { state: "pending" };
navigator.mediaDevices.getUserMedia({ video: true, audio: true })
  .then((s) => { window.__media = { state: "ok", tracks: s.getTracks().length }; })
  .catch((e) => { window.__media = { state: "error", error: String(e) }; });
</script></body></html>`))
	})

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		var state, errStr string
		var tracks int
		if err := chromedp.Run(ctx,
			chromedp.Navigate(h.URL),
			chromedp.Poll(`window.__media.state !== "pending"`, nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Evaluate(`window.__media.state`, &state),
			chromedp.Evaluate(`window.__media.tracks || 0`, &tracks),
			chromedp.Evaluate(`window.__media.error || ""`, &errStr),
		); err != nil {
			t.Fatalf("chromedp run: %v", err)
		}
		if state != "ok" {
			t.Fatalf("getUserMedia state = %q (err %q); fake media not working?", state, errStr)
		}
		if tracks < 2 {
			t.Fatalf("fake media tracks = %d, want >= 2 (video + audio)", tracks)
		}
	})
}
