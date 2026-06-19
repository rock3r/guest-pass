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

// T-10 / AC-10: the suspend cascade (D-27). The admin suspends a live foreign host with the
// "end live session now" option checked; the host is suspended AND its running session is ended, so
// it drops out of the live-sessions list. Server-rendered, no WebRTC (the DB session end is the
// observable cascade; the in-memory teardown is covered by web httptest).
func TestAdminConsole_SuspendCascadeEndsLiveSession(t *testing.T) {
	s := seedHostApp(t)
	ctx := context.Background()
	if err := s.store.SetHostAdmin(ctx, s.hostID, true); err != nil {
		t.Fatalf("promote to admin: %v", err)
	}
	hostB, err := s.store.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "cascade-foreign-sub", Email: "cascade@example.com", Name: "Cascade Host", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost(foreign): %v", err)
	}
	streamB, err := s.store.CreateStream(ctx, store.CreateStreamParams{HostID: hostB.ID, Title: "Cascade Live Show"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if _, err := s.store.StartSession(ctx, streamB.ID, hostB.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})
	suspendForm := `form[action="/api/admin/hosts/` + hostB.ID + `/suspend"]`

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var flash, sessionsText string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/admin"),
			// The live foreign host's row offers the cascade checkbox.
			chromedp.WaitVisible(suspendForm+` input[name="end_live"]`, chromedp.ByQuery),
			chromedp.Click(suspendForm+` input[name="end_live"]`, chromedp.ByQuery),
			chromedp.Click(suspendForm+` button[type="submit"]`, chromedp.ByQuery),
			// After the PRG redirect: the cascade flash, and the session is gone from the live list.
			chromedp.WaitVisible(`.admin-flash-ok`, chromedp.ByQuery),
			chromedp.Text(`.admin-flash-ok`, &flash, chromedp.ByQuery),
			chromedp.Text(`.admin-sessions`, &sessionsText, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("suspend cascade flow: %v", err)
		}
		if !strings.Contains(flash, "ended") {
			t.Fatalf("flash = %q, want it to confirm the session was ended", flash)
		}
		if strings.Contains(sessionsText, "Cascade Live Show") {
			t.Fatalf("the suspended host's session should be gone from the live list:\n%s", sessionsText)
		}
		if got, _ := s.store.GetHost(ctx, hostB.ID); got.Status != store.HostSuspended {
			t.Fatalf("host status = %q, want suspended", got.Status)
		}
		if _, err := s.store.ActiveSession(ctx, hostB.ID); err == nil {
			t.Fatal("the foreign host's session should be ended")
		}
	})
}
