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
	handler, err := web.NewRouter(web.RouterConfig{
		SourceURL: "https://github.com/rock3r/guest-pass/tree/test",
		Hub:       signaling.NewHub(nil, nil),
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
	sess, err := ring.Issue(host.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue host session: %v", err)
	}
	return &appSeed{store: st, ring: ring, base: Serve(t, handler).URL, hostID: host.ID, hostCkie: sess}
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
		var emptyText string
		if err := chromedp.Run(ctx,
			network.Enable(),
			setHostCookie,
			chromedp.Navigate(s.base+"/app"),
			chromedp.WaitVisible(`.stream-create`, chromedp.ByQuery),
			chromedp.Text(`.dash-empty`, &emptyText, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("load empty dashboard: %v", err)
		}
		if !strings.Contains(emptyText, "No streams") {
			t.Fatalf("empty dashboard text = %q, want the empty-state copy", emptyText)
		}

		// Create a stream from the server-rendered form.
		if err := chromedp.Run(ctx,
			chromedp.SendKeys(`.stream-create input[name="title"]`, "First Stream", chromedp.ByQuery),
			chromedp.Click(`.stream-create button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.stream-title`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("create stream: %v", err)
		}
		var title string
		if err := chromedp.Run(ctx, chromedp.Text(`.stream-title`, &title, chromedp.ByQuery)); err != nil {
			t.Fatalf("read created title: %v", err)
		}
		if !strings.Contains(title, "First Stream") {
			t.Fatalf("dashboard title = %q, want it to list the created stream", title)
		}

		// Edit it: open the edit form, clear + retype the title, save.
		if err := chromedp.Run(ctx,
			chromedp.Click(`.stream-card a.btn-quiet`, chromedp.ByQuery),
			chromedp.WaitVisible(`.stream-edit input[name="title"]`, chromedp.ByQuery),
			chromedp.Evaluate(`document.querySelector('.stream-edit input[name="title"]').value=''`, nil),
			chromedp.SendKeys(`.stream-edit input[name="title"]`, "Renamed Stream", chromedp.ByQuery),
			chromedp.Click(`.stream-form button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.stream-title`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("edit stream: %v", err)
		}
		var renamed string
		if err := chromedp.Run(ctx, chromedp.Text(`.stream-title`, &renamed, chromedp.ByQuery)); err != nil {
			t.Fatalf("read renamed title: %v", err)
		}
		if !strings.Contains(renamed, "Renamed Stream") {
			t.Fatalf("dashboard title after edit = %q, want Renamed Stream", renamed)
		}

		// Delete it: the dashboard returns to the empty state.
		var afterDelete string
		if err := chromedp.Run(ctx,
			chromedp.Click(`.stream-card .inline-form button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.dash-empty`, chromedp.ByQuery),
			chromedp.Text(`.dash-empty`, &afterDelete, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("delete stream: %v", err)
		}
		if !strings.Contains(afterDelete, "No streams") {
			t.Fatalf("dashboard after delete = %q, want the empty state", afterDelete)
		}
	})
}

// T-1 / AC-1 (EN-6): a pending host's session verifies but is not active, so the dashboard
// is gated — the host never reaches the stream UI.
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
		if !strings.Contains(strings.ToLower(bodyText), "forbidden") {
			t.Fatalf("pending host body = %q, want the forbidden gate", bodyText)
		}
	})
}
