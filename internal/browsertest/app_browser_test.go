//go:build browser

package browsertest

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/livecheck"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
	"github.com/rock3r/guest-pass/internal/web"
)

// appSeed is a running real router over a store seeded with one active host, plus the
// pieces a host-app browser test needs to drive the dashboard and to forge a non-active
// host's session (to prove the EN-6 gate).
type appSeed struct {
	store    *store.Store
	ring     *auth.KeyRing
	hub      *signaling.Hub
	base     string
	hostID   string
	hostCkie string
}

func seedHostApp(t *testing.T) *appSeed {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hostapp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	host, err := st.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "app-sub", Email: "host@example.com", Name: "Aria Host", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	hasher, err := token.NewHasher("hostapp-browser-token-secret-bbbbbbbb")
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	ring, err := auth.NewKeyRing("hostapp-browser-session-secret-cccccccc")
	if err != nil {
		t.Fatalf("key ring: %v", err)
	}
	hub := signaling.NewHub(nil, nil)
	handler, err := web.NewRouter(web.RouterConfig{
		SourceURL: "https://github.com/rock3r/guest-pass/tree/test",
		Hub:       hub,
		Auth:      auth.NewAuthenticator(ring, st, false),
		Store:     st,
		Hasher:    hasher,
		Mailer:    mail.NewLogMailer(io.Discard),
		BaseURL:   "https://gp.example",
		LiveCheck: livecheck.NewChecker(), // D-29: WatchURL/Normalize are pure (no network) on this path
		StaticDir: BuildDist(t),
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	sess, err := ring.Issue(host.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue host session: %v", err)
	}
	return &appSeed{store: st, ring: ring, hub: hub, base: Serve(t, handler).URL, hostID: host.ID, hostCkie: sess}
}

// T-1 / AC-1: the host app shell round-trips through real Chrome — the dashboard lists
// streams, the create form adds one (POST-redirect-GET), the edit form renames it, and
// the delete button removes it — all server-rendered with NO JavaScript (D-32).
func TestHostApp_DashboardCRUDRoundTrip(t *testing.T) {
	s := seedHostApp(t)

	setHostCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, s.hostCkie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		// Empty dashboard: the "up next" column shows its empty-state link.
		var emptyText string
		if err := chromedp.Run(ctx,
			network.Enable(),
			chromedp.EmulateViewport(1440, 900),
			setHostCookie,
			chromedp.Navigate(s.base+"/app"),
			chromedp.WaitVisible(`[data-dialog-open="new-stream-dialog"]`, chromedp.ByQuery),
			chromedp.Text(`.dash-grid`, &emptyText, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("load empty dashboard: %v", err)
		}
		if !strings.Contains(emptyText, "Schedule your first stream") {
			t.Fatalf("empty dashboard text = %q, want the empty-state copy", emptyText)
		}

		// The two dashboard columns use the same visual section rhythm: their first
		// surfaces must begin on one horizontal line. Keep this as a browser-level
		// assertion because the defect is caused by the interaction of default h2
		// margins, a flex heading wrapper, and the grid layout.
		var cardTopDelta float64
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`(() => {
				const upcoming = document.querySelector('.stream-tile-card');
				const recent = document.querySelector('.stream-recent-card');
				return Math.abs(upcoming.getBoundingClientRect().top - recent.getBoundingClientRect().top);
			})()`, &cardTopDelta),
		); err != nil {
			t.Fatalf("measure empty dashboard card alignment: %v", err)
		}
		if cardTopDelta > 1 {
			t.Fatalf("dashboard first-card top delta = %.1fpx, want ≤ 1px", cardTopDelta)
		}

		// Create a stream via the New stream dialog (the v2 dashboard's create flow).
		if err := chromedp.Run(ctx,
			chromedp.Click(`[data-dialog-open="new-stream-dialog"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`#new-stream-dialog input[name="title"]`, chromedp.ByQuery),
			chromedp.SendKeys(`#new-stream-dialog input[name="title"]`, "First Stream", chromedp.ByQuery),
			chromedp.Click(`#new-stream-dialog button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.stream-tile-title`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("create stream: %v", err)
		}
		var title, invitesHref string
		if err := chromedp.Run(ctx,
			chromedp.Text(`.stream-tile-title`, &title, chromedp.ByQuery),
			chromedp.AttributeValue(`.stream-tile-title`, "href", &invitesHref, nil),
		); err != nil {
			t.Fatalf("read created stream: %v", err)
		}
		if !strings.Contains(title, "First Stream") {
			t.Fatalf("dashboard tile = %q, want it to list the created stream", title)
		}
		// The tile links to the stream's invites; derive the id to drive edit/delete, which the
		// v2 design moves onto the stream edit page (the dashboard no longer has inline CRUD).
		id := strings.TrimSuffix(strings.TrimPrefix(invitesHref, "/app/streams/"), "/invites")
		editURL := s.base + "/app/streams/" + id + "/edit"

		// Edit on the stream edit page, then confirm the dashboard reflects the new title.
		if err := chromedp.Run(ctx,
			chromedp.Navigate(editURL),
			chromedp.WaitVisible(`.stream-form input[name="title"]`, chromedp.ByQuery),
			chromedp.Evaluate(`document.querySelector('.stream-form input[name="title"]').value=''`, nil),
			chromedp.SendKeys(`.stream-form input[name="title"]`, "Renamed Stream", chromedp.ByQuery),
			chromedp.Click(`.stream-form button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.stream-tile-title`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("edit stream: %v", err)
		}
		var renamed string
		if err := chromedp.Run(ctx, chromedp.Text(`.stream-tile-title`, &renamed, chromedp.ByQuery)); err != nil {
			t.Fatalf("read renamed title: %v", err)
		}
		if !strings.Contains(renamed, "Renamed Stream") {
			t.Fatalf("dashboard tile after edit = %q, want Renamed Stream", renamed)
		}

		// Delete from the edit page; the dashboard returns to the empty state.
		var afterDelete string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(editURL),
			chromedp.WaitVisible(`.stream-delete button[type="submit"]`, chromedp.ByQuery),
			chromedp.Click(`.stream-delete button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`[data-dialog-open="new-stream-dialog"]`, chromedp.ByQuery),
			chromedp.Text(`.dash-grid`, &afterDelete, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("delete stream: %v", err)
		}
		if !strings.Contains(afterDelete, "Schedule your first stream") {
			t.Fatalf("dashboard after delete = %q, want the empty state", afterDelete)
		}
	})
}

// T-1 / AC-1 (EN-6): a pending host's session verifies but is not active, so the dashboard
// is gated — the host never reaches the stream UI. M5.5: the gate is now an explanatory
// "awaiting approval" screen (a rendered navigation), not a bare "forbidden" body.
func TestHostApp_PendingHostGated(t *testing.T) {
	s := seedHostApp(t)
	pending, err := s.store.CreateHost(context.Background(), store.CreateHostParams{
		GoogleSub: "pending-sub", Email: "pending@example.com", Name: "Pat Pending", Status: store.HostPending,
	})
	if err != nil {
		t.Fatalf("CreateHost pending: %v", err)
	}
	tok, err := s.ring.Issue(pending.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue pending session: %v", err)
	}

	setPendingCookie := chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie(auth.SessionCookie, tok).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
	})

	Chrome(t, 60*time.Second, func(ctx context.Context) {
		var bodyText string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setPendingCookie,
			chromedp.Navigate(s.base+"/app"),
			chromedp.WaitReady(`body`, chromedp.ByQuery),
			chromedp.Evaluate(`document.body.innerText`, &bodyText),
		); err != nil {
			t.Fatalf("load gated dashboard: %v", err)
		}
		if strings.Contains(bodyText, "Your streams") || strings.Contains(bodyText, "Create a stream") {
			t.Fatalf("pending host reached the dashboard; body = %q", bodyText)
		}
		lower := strings.ToLower(bodyText)
		if !strings.Contains(lower, "approval") && !strings.Contains(lower, "pending") {
			t.Fatalf("pending host body = %q, want the awaiting-approval gate", bodyText)
		}
	})
}
