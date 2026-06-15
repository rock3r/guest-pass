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

// clickAndWaitNav clicks sel and blocks until the resulting top-level navigation has fully
// loaded the new page (the page's load event fires once). A no-JS form POST reloads the
// whole page, so a same-selector WaitVisible would match the pre-submit page and a
// chromedp.Poll would die when its JS context is torn down mid-navigation (-32000). Waiting
// on the load event lets the following assertion read the server-rendered reloaded page.
func clickAndWaitNav(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	loaded := make(chan struct{}, 1)
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	chromedp.ListenTarget(lctx, func(ev interface{}) {
		if _, ok := ev.(*page.EventLoadEventFired); ok {
			select {
			case loaded <- struct{}{}:
			default:
			}
		}
	})
	if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
		t.Fatalf("click %s: %v", sel, err)
	}
	select {
	case <-loaded:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for navigation after clicking %s", sel)
	}
}

// T-3 / AC-3: the Invites tab round-trips in real Chrome — the invite form exposes
// name/email/role ONLY (no production controls, EN-23), creating an invite reveals the
// magic link once, inline role edit promotes guest→co-host, and revoke turns the pass off.
func TestHostApp_InvitesTabRoundTrip(t *testing.T) {
	s := seedHostApp(t)
	stream, err := s.store.CreateStream(context.Background(), store.CreateStreamParams{
		HostID: s.hostID, Title: "Talk Show",
	})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	detail := s.base + "/app/streams/" + stream.ID

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		// Load the detail page; the invite form exposes name/email/role only (EN-23).
		var formHTML string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(detail),
			chromedp.WaitVisible(`.invite-create form`, chromedp.ByQuery),
			chromedp.OuterHTML(`.invite-create form`, &formHTML, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("load detail: %v", err)
		}
		for _, want := range []string{`name="name"`, `name="email"`, `name="role"`} {
			if !strings.Contains(formHTML, want) {
				t.Fatalf("invite form missing %s; form:\n%s", want, formHTML)
			}
		}
		for _, forbidden := range []string{"can_screen", `name="slot"`, "screenshare"} {
			if strings.Contains(formHTML, forbidden) {
				t.Fatalf("invite form leaked a production control %q (EN-23)", forbidden)
			}
		}

		// Create an invite; the magic link is revealed once.
		var issuedLink string
		if err := chromedp.Run(ctx,
			chromedp.SendKeys(`.invite-create input[name="name"]`, "Dana", chromedp.ByQuery),
			chromedp.SendKeys(`.invite-create input[name="email"]`, "dana@example.com", chromedp.ByQuery),
			chromedp.Click(`.invite-create button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.issued-link`, chromedp.ByQuery),
			chromedp.Value(`.issued-link`, &issuedLink, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("create invite: %v", err)
		}
		if !strings.Contains(issuedLink, "/p/") {
			t.Fatalf("revealed link = %q, want a /p/<token> magic link", issuedLink)
		}

		// The new invite shows in the table; promote guest → co-host inline. SetValue changes
		// the select client-side; the submit navigates. After the reload completes we read the
		// SERVER-rendered selection (option.selected on the fresh page) — proving persistence,
		// not just our own client-side SetValue.
		if err := chromedp.Run(ctx, chromedp.WaitVisible(`.invite-table tbody tr`, chromedp.ByQuery),
			chromedp.SetValue(`.invite-table tbody tr .role-form select`, "cohost", chromedp.ByQuery),
		); err != nil {
			t.Fatalf("prepare role edit: %v", err)
		}
		clickAndWaitNav(t, ctx, `.invite-table tbody tr .role-form button[type="submit"]`)
		var promoted bool
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`(() => { const o = document.querySelector('.invite-table tbody tr .role-form option[value="cohost"]'); return !!o && o.selected; })()`, &promoted),
		); err != nil {
			t.Fatalf("read role after promote: %v", err)
		}
		if !promoted {
			t.Fatal("inline role edit did not persist guest→co-host")
		}

		// Revoke the invite; after the reload, the pass reads "Revoked" and its revoke button
		// is disabled (server-rendered Retired state — the client cannot pre-set it).
		clickAndWaitNav(t, ctx, `.invite-table tbody tr .invite-actions form:last-child button`)
		var statusText string
		var revokeDisabled bool
		if err := chromedp.Run(ctx,
			chromedp.Text(`.invite-table tbody tr .stream-status`, &statusText, chromedp.ByQuery),
			chromedp.Evaluate(`!!document.querySelector('.invite-table tbody tr .invite-actions form:last-child button[disabled]')`, &revokeDisabled),
		); err != nil {
			t.Fatalf("read status after revoke: %v", err)
		}
		// The pill text-transforms to uppercase in CSS, so compare case-insensitively.
		if !strings.Contains(strings.ToLower(statusText), "revoked") {
			t.Fatalf("status after revoke = %q, want Revoked", statusText)
		}
		if !revokeDisabled {
			t.Fatal("revoke button should be disabled once the pass is revoked")
		}
	})
}
