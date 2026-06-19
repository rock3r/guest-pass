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

// reportSeed is a running router with a distinct ADMIN host and a separate REPORTED host that has a
// stream + an invited guest whose magic-link token can drive the public report form.
type reportSeed struct {
	store        *store.Store
	base         string
	adminCookie  string // admin host session
	reportedID   string // the reported (sending) host's id
	guestToken   string // the invited guest's raw magic-link token (/p/{token}/report)
	reporterMail string // the guest's email (becomes the report's reporter identity)
}

func seedReportFlow(t *testing.T) *reportSeed {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	admin, err := st.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "report-admin-sub", Email: "admin@example.com", Name: "Admin", Status: store.HostActive, IsAdmin: true,
	})
	if err != nil {
		t.Fatalf("CreateHost(admin): %v", err)
	}
	reported, err := st.CreateHost(ctx, store.CreateHostParams{
		GoogleSub: "reported-sub", Email: "reported@example.com", Name: "Reported Host", Status: store.HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost(reported): %v", err)
	}
	stream, err := st.CreateStream(ctx, store.CreateStreamParams{HostID: reported.ID, Title: "Suspicious Stream"})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	hasher, err := token.NewHasher("report-browser-token-secret-bbbbbbbbbb")
	if err != nil {
		t.Fatalf("hasher: %v", err)
	}
	raw, err := token.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	const reporter = "reporter@example.com"
	rep := reporter
	if _, err := st.CreatePass(ctx, store.CreatePassParams{
		StreamID: stream.ID, Name: ptr("Reporter"), Email: &rep, Role: store.RoleGuest,
		TokenHash: hasher.Hash(raw), Status: store.PassSent,
	}); err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	ring, err := auth.NewKeyRing("report-browser-session-secret-cccccccccc")
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
	adminSess, err := ring.Issue(admin.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue admin session: %v", err)
	}
	return &reportSeed{
		store: st, base: Serve(t, handler).URL, adminCookie: adminSess,
		reportedID: reported.ID, guestToken: raw, reporterMail: reporter,
	}
}

// T-11 / AC-11: the full abuse-report flow — a guest submits the public report-invite form, the
// admin reviews it on the console (full detail, reporter included), and suspends the reported host
// from that review. Server-rendered, no WebRTC.
func TestReportFlow_SubmitReviewSuspend(t *testing.T) {
	s := seedReportFlow(t)

	Chrome(t, 90*time.Second, func(ctx context.Context) {
		// 1) The guest submits the report (no auth).
		var thanks string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(s.base+"/p/"+s.guestToken+"/report"),
			chromedp.WaitVisible(`.report-form`, chromedp.ByQuery),
			chromedp.SetValue(`.report-form select[name="category"]`, "phishing", chromedp.ByQuery),
			chromedp.SendKeys(`.report-form textarea[name="message"]`, "this invite is a phishing scam", chromedp.ByQuery),
			chromedp.Click(`.report-form button[type="submit"]`, chromedp.ByQuery),
			// The thank-you page has no form — wait for it to go before reading the heading (the PRG
			// redirect renders a fresh page; the form re-renders only on a validation error).
			chromedp.WaitNotPresent(`.report-form`, chromedp.ByQuery),
			chromedp.Text(`.report h1`, &thanks, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("guest report submit: %v", err)
		}
		if !strings.Contains(strings.ToLower(thanks), "sent") {
			t.Fatalf("after submit heading = %q, want a thank-you", thanks)
		}

		// 2) The admin reviews the report on the console (full detail), then 3) suspends from it.
		setAdmin := chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(auth.SessionCookie, s.adminCookie).WithURL(s.base).WithHTTPOnly(true).Do(ctx)
		})
		var reportsText, flash string
		suspendForm := `.admin-reports article[data-report-host="` + s.reportedID + `"] form[action="/api/admin/hosts/` + s.reportedID + `/suspend"]`
		if err := chromedp.Run(ctx,
			network.Enable(),
			setAdmin,
			chromedp.Navigate(s.base+"/admin"),
			chromedp.WaitVisible(`.admin-reports`, chromedp.ByQuery),
			chromedp.Text(`.admin-reports`, &reportsText, chromedp.ByQuery),
			chromedp.Click(suspendForm+` button[type="submit"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.admin-flash-ok`, chromedp.ByQuery),
			chromedp.Text(`.admin-flash-ok`, &flash, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("admin review + suspend: %v", err)
		}
		for _, want := range []string{s.reporterMail, "Phishing", "this invite is a phishing scam", "Suspicious Stream"} {
			if !strings.Contains(reportsText, want) {
				t.Fatalf("admin reports view missing %q:\n%s", want, reportsText)
			}
		}
		if !strings.Contains(strings.ToLower(flash), "suspended") {
			t.Fatalf("flash = %q, want a suspended confirmation", flash)
		}
		if got, _ := s.store.GetHost(context.Background(), s.reportedID); got.Status != store.HostSuspended {
			t.Fatalf("reported host status = %q, want suspended", got.Status)
		}
	})
}
