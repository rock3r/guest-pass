package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

// A public report submit resolves the reporter (the invited guest's email) + host + stream from the
// token SERVER-SIDE — never from the form. Only category + message are taken from the request, so a
// reporter can't forge who they are or whom they report (D-42/EN-24).
func TestReport_SubmitResolvesReporterFromToken(t *testing.T) {
	a := newAPIHarness(t)
	_, hostCookie := a.host(t, "rep-host")
	streamID := a.createStream(t, hostCookie, "Reported Stream")
	const reporter = "victim@example.com"
	raw, err := tokenMintFor(t, a, streamID, "Victim", reporter)
	if err != nil {
		t.Fatalf("mint pass: %v", err)
	}

	// A spoofed reporter_email / host_id in the body must be ignored.
	body := "category=phishing&message=this looks like a scam&reporter_email=attacker@evil.test&host_id=someoneelse"
	rec := a.formPost(t, "/p/"+raw+"/report", body, nil)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "sent=1") {
		t.Fatalf("report submit = %d loc=%q, want 303 → sent=1", rec.Code, rec.Header().Get("Location"))
	}

	reports, err := a.store.ListReports(context.Background())
	if err != nil || len(reports) != 1 {
		t.Fatalf("ListReports = %d reports, %v; want 1", len(reports), err)
	}
	got := reports[0]
	if got.ReporterEmail == nil || *got.ReporterEmail != reporter {
		t.Fatalf("reporter = %v, want the pass email %q (server-resolved, not form input)", got.ReporterEmail, reporter)
	}
	if got.Category != store.ReportPhishing || got.Message == nil || *got.Message != "this looks like a scam" {
		t.Fatalf("report content = %+v, want phishing + the message", got)
	}
	// The stream's host is the reported host — not the spoofed host_id.
	stream, _ := a.store.GetStream(context.Background(), streamID)
	if got.HostID != stream.HostID {
		t.Fatalf("host_id = %q, want the resolved host %q", got.HostID, stream.HostID)
	}
}

// Both category and message are required; an invalid/empty submission records nothing.
func TestReport_BothRequired(t *testing.T) {
	a := newAPIHarness(t)
	_, hostCookie := a.host(t, "req-host")
	streamID := a.createStream(t, hostCookie, "S")
	raw, _ := tokenMintFor(t, a, streamID, "G", "g@example.com")

	for _, body := range []string{
		"category=phishing&message=",   // no message
		"category=&message=hello",      // no category
		"category=bogus&message=hello", // invalid category
		"message=hello",                // missing category entirely
	} {
		rec := a.formPost(t, "/p/"+raw+"/report", body, nil)
		if !strings.Contains(rec.Header().Get("Location"), "error=1") {
			t.Fatalf("invalid submit %q loc=%q, want error=1", body, rec.Header().Get("Location"))
		}
	}
	if reports, _ := a.store.ListReports(context.Background()); len(reports) != 0 {
		t.Fatalf("invalid submits recorded %d reports, want 0", len(reports))
	}
}

// An unknown token is 404 on both the form and the submit (opaque, like the pass page).
func TestReport_UnknownToken404(t *testing.T) {
	a := newAPIHarness(t)
	if rec := a.req(t, http.MethodGet, "/p/nope-token/report", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown report form = %d, want 404", rec.Code)
	}
	if rec := a.formPost(t, "/p/nope-token/report", "category=spam&message=x", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("POST unknown report = %d, want 404", rec.Code)
	}
}

// The admin console renders the reports grouped per reported host with full detail (reporter
// included — admin-only), and Dismiss all clears them. Authority: only admins.
func TestReport_AdminReviewAndDismiss(t *testing.T) {
	a := newAPIHarness(t)
	_, adminCookie := a.adminHost(t, "report-admin")
	_, hostCookie := a.host(t, "reported-host")
	streamID := a.createStream(t, hostCookie, "Bad Stream")
	const reporter = "tipster@example.com"
	raw, _ := tokenMintFor(t, a, streamID, "Tipster", reporter)
	a.formPost(t, "/p/"+raw+"/report", "category=harassment&message=they harassed me", nil)

	stream, _ := a.store.GetStream(context.Background(), streamID)
	body := a.req(t, http.MethodGet, "/admin", "", adminCookie).Body.String()
	for _, want := range []string{reporter, "Harassment", "they harassed me", "Bad Stream"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin console missing report detail %q:\n%s", want, body)
		}
	}

	// Non-admin cannot dismiss; admin can.
	if rec := a.formPost(t, "/api/admin/abuse-reports/"+stream.HostID+"/dismiss", "", hostCookie); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin dismiss = %d, want 403", rec.Code)
	}
	rec := a.formPost(t, "/api/admin/abuse-reports/"+stream.HostID+"/dismiss", "", adminCookie)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "dismissed") {
		t.Fatalf("dismiss = %d loc=%q, want 303 → dismissed", rec.Code, rec.Header().Get("Location"))
	}
	if reports, _ := a.store.ListReports(context.Background()); len(reports) != 0 {
		t.Fatalf("after dismiss, %d reports remain, want 0", len(reports))
	}
}

// EN-24 retaliation guard: the reported host never learns that one of their invitees reported them.
// The host legitimately sees their own invite list (so the reporter's email — their own invitee —
// is not itself a secret), but the REPORT — its existence, the complaint text, the linkage — lives
// only on the admin console (RequireAdmin). A regular host can't reach it, and no host surface
// carries the complaint text.
func TestReport_ReporterHiddenFromHost(t *testing.T) {
	a := newAPIHarness(t)
	host, hostCookie := a.host(t, "watched-host")
	streamID := a.createStream(t, hostCookie, "Watched Stream")
	const secretMsg = "the-secret-complaint-text-xyzzy"
	raw, _ := tokenMintFor(t, a, streamID, "Anon", "reporter@example.com")
	a.formPost(t, "/p/"+raw+"/report", "category=spam&message="+secretMsg, nil)
	_ = host

	// The reported host is not an admin → the admin console (the only report surface) is forbidden.
	if rec := a.req(t, http.MethodGet, "/admin", "", hostCookie); rec.Code != http.StatusForbidden {
		t.Fatalf("reported host GET /admin = %d, want 403", rec.Code)
	}
	// None of the host's own surfaces carry the complaint text or any report markup.
	for _, path := range []string{"/app", "/app/streams/" + streamID} {
		body := a.req(t, http.MethodGet, path, "", hostCookie).Body.String()
		if strings.Contains(body, secretMsg) || strings.Contains(body, "abuse-report") {
			t.Fatalf("host surface %s leaked the report / complaint:\n%s", path, body)
		}
	}
}
