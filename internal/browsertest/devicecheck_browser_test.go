//go:build browser

package browsertest

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
	"github.com/rock3r/guest-pass/internal/web"
)

func ptr[T any](v T) *T { return &v }

// seedDeviceCheck builds a real router backed by a seeded store with one active host, a
// stream, and a sent pass. It returns the store, the server base URL, the pass's raw
// magic-link token, and the pass id.
func seedDeviceCheck(t *testing.T) (st *store.Store, baseURL, rawToken, passID string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "devcheck.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	host, err := st.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "dc-sub", Email: "host@example.com", Name: "Host", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	stream, err := st.CreateStream(ctx, store.CreateStreamParams{HostID: host.ID, Title: "Device Check Stream"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	hasher, err := token.NewHasher("devcheck-browser-token-secret-bbbbbbbb")
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	raw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	pass, err := st.CreatePass(ctx, store.CreatePassParams{
		StreamID: stream.ID, Name: ptr("Dana"), Role: store.RoleGuest, TokenHash: hasher.Hash(raw), Status: store.PassSent,
	})
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}

	ring, err := auth.NewKeyRing("devcheck-browser-session-secret-cccccccc")
	if err != nil {
		t.Fatalf("key ring: %v", err)
	}
	handler, err := web.NewRouter(web.RouterConfig{
		SourceURL: "https://github.com/rock3r/guest-pass/tree/test",
		Auth:      auth.NewAuthenticator(ring, st, false),
		Store:     st,
		Hasher:    hasher,
		Mailer:    mail.NewLogMailer(io.Discard),
		BaseURL:   "https://gp.example",
		StaticDir: BuildDist(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return st, Serve(t, handler).URL, raw, pass.ID
}

// T-6: the device-check island renders a live cam/mic preview (getUserMedia), a bare GET of
// the magic link does NOT mark the pass opened (EN-10), and the explicit "enter" action
// transitions it to opened. Driven in real Chrome with fake media.
func TestDeviceCheck_PreviewAndExplicitEntry(t *testing.T) {
	st, base, raw, passID := seedDeviceCheck(t)
	ctx := context.Background()

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		// 1. GET the magic link → the island appears, but the pass is NOT marked opened.
		if err := chromedp.Run(cctx,
			chromedp.Navigate(base+"/p/"+raw),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("navigate: %v", err)
		}
		if p, _ := st.GetPass(ctx, passID); p.Status != store.PassSent || p.OpenedAt != nil {
			t.Fatalf("a bare GET must not mark opened (EN-10): status=%q openedAt=%v", p.Status, p.OpenedAt)
		}

		// 2. Start the camera check → a live preview with real (fake-device) frames. Capture
		// the live video track so we can confirm it is released after entry.
		var trackState string
		if err := chromedp.Run(cctx,
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Evaluate(`(() => { window.__dcTrack = document.querySelector('.dc-video').srcObject.getVideoTracks()[0]; return window.__dcTrack.readyState; })()`, &trackState),
		); err != nil {
			t.Fatalf("preview did not render live frames: %v", err)
		}
		if trackState != "live" {
			t.Fatalf("preview camera track = %q, want live", trackState)
		}

		// 3. Explicit entry → the pass transitions to opened AND the camera is released
		// (the captured track ends), so the device light goes off before the greenroom.
		if err := chromedp.Run(cctx,
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-entered="1"]`, chromedp.ByQuery),
			chromedp.Poll(`window.__dcTrack && window.__dcTrack.readyState === "ended"`,
				nil, chromedp.WithPollingTimeout(10*time.Second)),
		); err != nil {
			t.Fatalf("entry did not complete (or camera not released): %v", err)
		}
		p, err := st.GetPass(ctx, passID)
		if err != nil {
			t.Fatalf("GetPass: %v", err)
		}
		if p.Status != store.PassOpened || p.OpenedAt == nil {
			t.Fatalf("after entry, status=%q openedAt=%v, want opened + stamped", p.Status, p.OpenedAt)
		}
	})
}

// A failed entry must not leave the camera running behind the error UI: the preview track
// is released even when the entry POST fails (here forced by retiring the pass server-side).
func TestDeviceCheck_EntryFailureReleasesCamera(t *testing.T) {
	st, base, raw, passID := seedDeviceCheck(t)
	ctx := context.Background()

	Chrome(t, 120*time.Second, func(cctx context.Context) {
		var trackState string
		if err := chromedp.Run(cctx,
			chromedp.Navigate(base+"/p/"+raw),
			chromedp.WaitVisible(`.dc-start`, chromedp.ByQuery),
			chromedp.Click(`.dc-start`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-video`, chromedp.ByQuery),
			chromedp.Poll(`!!document.querySelector('.dc-video') && document.querySelector('.dc-video').videoWidth > 0`,
				nil, chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Evaluate(`(() => { window.__dcTrack = document.querySelector('.dc-video').srcObject.getVideoTracks()[0]; return window.__dcTrack.readyState; })()`, &trackState),
		); err != nil {
			t.Fatalf("preview: %v", err)
		}
		if trackState != "live" {
			t.Fatalf("preview camera track = %q, want live", trackState)
		}

		// Retire the pass server-side so the entry POST fails (410).
		if err := st.SetPassStatus(ctx, passID, store.PassRevoked); err != nil {
			t.Fatalf("SetPassStatus: %v", err)
		}

		if err := chromedp.Run(cctx,
			chromedp.Click(`.dc-enter`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dc-error`, chromedp.ByQuery),
			chromedp.Poll(`window.__dcTrack && window.__dcTrack.readyState === "ended"`,
				nil, chromedp.WithPollingTimeout(10*time.Second)),
		); err != nil {
			t.Fatalf("a failed entry must release the camera: %v", err)
		}
		if p, _ := st.GetPass(ctx, passID); p.Status == store.PassOpened {
			t.Fatalf("a failed entry must not mark opened, got %q", p.Status)
		}
	})
}
