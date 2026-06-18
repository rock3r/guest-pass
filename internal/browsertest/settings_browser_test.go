//go:build browser

package browsertest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// T-5 / AC-3,4: the host settings page renders a read-only account card and the FUNCTIONAL GDPR
// self-service controls — amend (rectification) persists, and export returns the host's PII as a
// download. No JS rides the page (D-32); the controls are server-rendered forms + a link.
func TestHostApp_SettingsGDPR_AmendAndExport(t *testing.T) {
	s := seedHostApp(t)
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var email string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/app/settings"),
			chromedp.WaitVisible(`.settings-account`, chromedp.ByQuery),
			chromedp.Text(`.settings-email`, &email, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("load settings page: %v", err)
		}
		if !strings.Contains(email, "host@example.com") {
			t.Fatalf("account card email = %q, want the host's email", email)
		}

		// Amend (rectification): set a new display name, submit, land on ?saved=1 with the flash and
		// the input reflecting the persisted value.
		var savedFlash bool
		var nameValue string
		if err := chromedp.Run(ctx,
			chromedp.SetValue(`#settings-name`, "Amended Name", chromedp.ByQuery),
			chromedp.Click(`.settings-amend button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-flash="saved"]`, chromedp.ByQuery),
			chromedp.Evaluate(`!!document.querySelector('[data-flash="saved"]')`, &savedFlash),
			chromedp.Value(`#settings-name`, &nameValue, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("amend name: %v", err)
		}
		if !savedFlash || nameValue != "Amended Name" {
			t.Fatalf("after amend: savedFlash=%v nameValue=%q, want saved + persisted name", savedFlash, nameValue)
		}
		if got, _ := s.store.GetHost(context.Background(), s.hostID); got.Name != "Amended Name" {
			t.Fatalf("amend did not persist: store name = %q", got.Name)
		}

		// Export download: the in-page fetch (same-origin, carries the cookie) returns the host's PII
		// JSON and never a token hash. The export link is present and points at the API route.
		var exportHref string
		var exportBody string
		if err := chromedp.Run(ctx,
			chromedp.AttributeValue(`[data-gdpr="export"]`, "href", &exportHref, nil),
			chromedp.Evaluate(`fetch('/api/me/export').then(r => r.ok ? r.text() : 'ERR:'+r.status)`, &exportBody,
				func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
		); err != nil {
			t.Fatalf("export fetch: %v", err)
		}
		if exportHref != "/api/me/export" {
			t.Fatalf("export link href = %q, want /api/me/export", exportHref)
		}
		if !strings.Contains(exportBody, "host@example.com") {
			t.Fatalf("export body missing the host's account email:\n%s", exportBody)
		}
		if strings.Contains(exportBody, "token_hash") {
			t.Fatalf("export leaked a token hash:\n%s", exportBody)
		}
	})
}

// T-5 / AC-5: the settings delete form erases the account — but is BLOCKED while a live session
// exists (D-M5-3), and only succeeds once the host is no longer live.
func TestHostApp_SettingsGDPR_DeleteBlockedWhileLiveThenErases(t *testing.T) {
	s := seedHostApp(t)
	ctx := context.Background()
	stream, err := s.store.CreateStream(ctx, store.CreateStreamParams{HostID: s.hostID, Title: "Live Show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if _, err := s.store.StartSession(ctx, stream.ID, s.hostID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	setHostCookie := chromedp.ActionFunc(func(c context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(c)
	})

	Chrome(t, 60*time.Second, func(cctx context.Context) {
		// Attempt delete while live → blocked, routed back with the live-error flash; host survives.
		var liveErr bool
		if err := chromedp.Run(cctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/app/settings"),
			chromedp.WaitVisible(`.settings-delete`, chromedp.ByQuery),
			chromedp.Click(`.settings-delete input[type="checkbox"]`, chromedp.ByQuery),
			chromedp.Click(`.settings-delete button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-flash="live-error"]`, chromedp.ByQuery),
			chromedp.Evaluate(`!!document.querySelector('[data-flash="live-error"]')`, &liveErr),
		); err != nil {
			t.Fatalf("delete-while-live: %v", err)
		}
		if !liveErr {
			t.Fatal("deleting while live must be blocked with the live-error flash")
		}
		if _, err := s.store.GetHost(ctx, s.hostID); err != nil {
			t.Fatalf("host must survive a blocked delete: %v", err)
		}

		// End the session, then delete succeeds → redirected to the public landing, host erased.
		if err := s.store.EndActiveSession(ctx, s.hostID); err != nil {
			t.Fatalf("EndActiveSession: %v", err)
		}
		var loc string
		if err := chromedp.Run(cctx,
			chromedp.Navigate(s.base+"/app/settings"),
			chromedp.WaitVisible(`.settings-delete`, chromedp.ByQuery),
			chromedp.Click(`.settings-delete input[type="checkbox"]`, chromedp.ByQuery),
			chromedp.Click(`.settings-delete button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`body`, chromedp.ByQuery),
			chromedp.Location(&loc),
		); err != nil {
			t.Fatalf("delete erase: %v", err)
		}
		if strings.Contains(loc, "/app/settings") {
			t.Fatalf("after a successful delete the host should leave the settings page, still at %q", loc)
		}
		if _, err := s.store.GetHost(ctx, s.hostID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("account not erased: GetHost = %v, want ErrNotFound", err)
		}
	})
}
