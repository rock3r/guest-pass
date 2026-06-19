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
	"github.com/rock3r/guest-pass/internal/store"
)

// T-9 / AC-9: the admin console renders cross-host live-session + host METADATA. We promote the
// seeded host to admin (is_admin is read live per request, EN-6), seed a FOREIGN host with a live
// session, then load /admin and assert the foreign session's metadata is shown — while the page
// carries no guest PII (there is none to show; the guest's name/email never reach this surface,
// §7.7). Server-rendered, no WebRTC.
func TestAdminConsole_RendersCrossHostMetadata(t *testing.T) {
	s := seedHostApp(t)
	ctx := context.Background()
	if err := s.store.SetHostAdmin(ctx, s.hostID, true); err != nil {
		t.Fatalf("promote to admin: %v", err)
	}

	// Foreign host B with a live session.
	hostB, err := s.store.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "admin-foreign-sub", Email: "foreign@example.com", Name: "Foreign Host", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost(foreign): %v", err)
	}
	streamB, err := s.store.CreateStream(ctx, store.CreateStreamParams{HostID: hostB.ID, Title: "Foreign Live Show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if _, err := s.store.StartSession(ctx, streamB.ID, hostB.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var sessionsText, hostsText, liveCount string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/admin"),
			chromedp.WaitVisible(`.admin-sessions`, chromedp.ByQuery),
			chromedp.Text(`.admin-sessions`, &sessionsText, chromedp.ByQuery),
			chromedp.Text(`.admin-hosts`, &hostsText, chromedp.ByQuery),
			chromedp.Text(`[data-stat="live-sessions"]`, &liveCount, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("admin console flow: %v", err)
		}
		if !strings.Contains(sessionsText, "Foreign Live Show") || !strings.Contains(sessionsText, "Foreign Host") {
			t.Fatalf("admin sessions table missing foreign-session metadata:\n%s", sessionsText)
		}
		if !strings.Contains(hostsText, "foreign@example.com") {
			t.Fatalf("admin hosts table missing the foreign host:\n%s", hostsText)
		}
		if strings.TrimSpace(liveCount) != "1" {
			t.Fatalf("live-sessions stat = %q, want 1", liveCount)
		}
	})
}
