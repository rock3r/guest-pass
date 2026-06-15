//go:build browser

package browsertest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// T-5 / AC-5: rotating a slot's source token tears down the live /s/{slot} OBS source — the
// source page receives a TERMINAL token-rotated terminate and STOPS (it does not reconnect
// with the now-dead token). The old-token-invalidation half of T-5 is covered by the web
// httptest (TestApp_RegenerateSlotRotatesToken); this is the live-teardown half.
func TestSlotRotation_TearsDownLiveSource(t *testing.T) {
	s := seedDeviceCheck(t)

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		// The OBS source page connects with the slot token (creating the host's live room).
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/s/"+s.slotLabel+"?token="+s.srcToken),
			chromedp.WaitVisible(`#obs-video`, chromedp.ByQuery),
			// data-obs-connected flips to "1" once the signaling socket is live, so the server has
			// joined this source to the room before we rotate.
			chromedp.Poll(`document.documentElement.dataset.obsConnected === "1"`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("obs source connect: %v", err)
		}

		// The host rotates THIS slot's token via the regenerate endpoint (host-authenticated).
		regen := s.base + "/app/streams/" + s.streamID + "/sources/slots/" + s.slotID + "/regenerate"
		req, _ := http.NewRequest(http.MethodPost, regen, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: s.hostCookie})
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("regenerate POST: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("regenerate = %d, want 303", resp.StatusCode)
		}

		// The OBS source receives the token-rotated terminate and STOPS (terminal state set).
		if err := chromedp.Run(ctx,
			chromedp.Poll(`document.documentElement.dataset.obsTerminated === "token-rotated"`, nil, chromedp.WithPollingTimeout(30*time.Second)),
		); err != nil {
			t.Fatalf("obs source did not tear down on token-rotated: %v", err)
		}
		// The reconnect loop stopped — it is no longer reporting connected.
		var connected string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.dataset.obsConnected || ""`, &connected)); err != nil {
			t.Fatalf("read obsConnected: %v", err)
		}
		if connected == "1" {
			t.Fatal("source is still connected after a terminal token-rotated terminate")
		}
	})
}
