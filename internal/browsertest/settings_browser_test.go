//go:build browser

package browsertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
)

// T-10 / AC-10: the host settings page renders a READ-ONLY account card (the host's Google
// identity), a pointer to the per-stream quality ceiling, and NON-FUNCTIONAL GDPR stub entry points
// (export / amend / delete) whose copy states self-service lands in a later release (D-37 — no
// functional purge in M4). All server-rendered, no JS (D-32).
func TestHostApp_SettingsAndGDPRStubs(t *testing.T) {
	s := seedHostApp(t)
	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var name, email string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/app/settings"),
			chromedp.WaitVisible(`.settings-account`, chromedp.ByQuery),
			chromedp.Text(`.settings-name`, &name, chromedp.ByQuery),
			chromedp.Text(`.settings-email`, &email, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("load settings page: %v", err)
		}
		if !strings.Contains(name, "Aria Host") {
			t.Fatalf("account card name = %q, want it to show the host's name", name)
		}
		if !strings.Contains(email, "host@example.com") {
			t.Fatalf("account card email = %q, want it to show the host's email", email)
		}

		// The three GDPR stub entry points exist, are DISABLED (non-functional), and the section
		// states self-service lands in a later release.
		var exportDisabled, amendDisabled, deleteDisabled bool
		var gdprText string
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(`[data-gdpr="export"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-gdpr="amend"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-gdpr="delete"]`, chromedp.ByQuery),
			chromedp.Evaluate(`document.querySelector('[data-gdpr="export"]').disabled`, &exportDisabled),
			chromedp.Evaluate(`document.querySelector('[data-gdpr="amend"]').disabled`, &amendDisabled),
			chromedp.Evaluate(`document.querySelector('[data-gdpr="delete"]').disabled`, &deleteDisabled),
			chromedp.Text(`.settings-data`, &gdprText, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("load GDPR stub entry points: %v", err)
		}
		if !exportDisabled || !amendDisabled || !deleteDisabled {
			t.Fatalf("GDPR entry points must be non-functional (disabled): export=%v amend=%v delete=%v", exportDisabled, amendDisabled, deleteDisabled)
		}
		if !strings.Contains(strings.ToLower(gdprText), "later release") {
			t.Fatalf("the GDPR section must state self-service lands in a later release; got %q", gdprText)
		}

		// The Settings nav link is marked current on this page.
		var navCurrent bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`!!document.querySelector('.app-links a[href="/app/settings"][aria-current="page"]')`, &navCurrent,
		)); err != nil {
			t.Fatalf("read settings nav state: %v", err)
		}
		if !navCurrent {
			t.Fatalf("the Settings nav link should be aria-current on the settings page")
		}
	})
}
