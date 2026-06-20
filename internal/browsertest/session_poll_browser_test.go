//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// M5.5: the host stream-detail page is no-JS server-rendered, so its "● Live" pill is computed once
// at render. When an admin suspends + force-ends the session (D-27 cascade) the pill went STALE with
// no indication until a manual refresh. A minimal read-only poll now swaps it for an "ended" notice
// IN PLACE — no full reload (so a half-typed invite survives). This drives the exact reported
// scenario: load the live page, suspend+end the host out from under it, assert the swap.
func TestStreamDetail_LivePollSwapsWhenForceEnded(t *testing.T) {
	s := seedHostApp(t)
	ctx := context.Background()
	stream, err := s.store.CreateStream(ctx, store.CreateStreamParams{HostID: s.hostID, Title: "Live Show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if _, err := s.store.StartSession(ctx, stream.ID, s.hostID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	// Poll fast so the test doesn't wait the 20s production interval. Injected before the page's
	// inline script runs (test-only seam; no production knob).
	fastPoll := chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument("window.__gpSessionPollMs = 300;").Do(ctx)
		return err
	})

	Chrome(t, 60*time.Second, func(cctx context.Context) {
		// The live detail page renders the "● Live" pill (and starts the poll).
		if err := chromedp.Run(cctx,
			network.Enable(),
			setHostCookie,
			fastPoll,
			chromedp.Navigate(s.base+"/app/streams/"+stream.ID),
			chromedp.WaitVisible(`#session-state .live-pill`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("live detail page did not render the live pill: %v", err)
		}

		// Admin suspends + force-ends the session (the D-27 cascade's DB effect).
		if err := s.store.SetHostStatus(ctx, s.hostID, store.HostSuspended); err != nil {
			t.Fatalf("SetHostStatus: %v", err)
		}
		if err := s.store.EndActiveSession(ctx, s.hostID); err != nil {
			t.Fatalf("EndActiveSession: %v", err)
		}

		// The poll detects it and swaps the pill for an "ended" notice in place — no manual refresh.
		var noteText string
		if err := chromedp.Run(cctx,
			chromedp.WaitVisible(`#session-state .live-note`, chromedp.ByQuery),
			chromedp.Text(`#session-state .live-note`, &noteText, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("the stale live pill never updated to an ended notice: %v", err)
		}
		if !strings.Contains(strings.ToLower(noteText), "ended") {
			t.Fatalf("ended notice text = %q, want a session-ended message", noteText)
		}
		// The stale "● Live" pill is gone.
		var pillCount int
		if err := chromedp.Run(cctx, chromedp.Evaluate(`document.querySelectorAll('#session-state .live-pill').length`, &pillCount)); err != nil {
			t.Fatalf("count live-pill: %v", err)
		}
		if pillCount != 0 {
			t.Fatalf("the stale live pill is still shown after the session ended")
		}
	})
}
