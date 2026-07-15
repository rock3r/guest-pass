package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// formReq submits a form-encoded request with the optional host cookie, mirroring how a
// browser posts the server-rendered dashboard forms (no JSON, no JS — D-32 / CONVENTIONS
// §3.1). It does NOT follow redirects, so a test can assert the POST-redirect-GET status
// and Location directly.
func (a *apiHarness) formReq(t *testing.T, method, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body string
	if form != nil {
		body = form.Encode()
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// hostWithStatus creates a host in the given lifecycle status and returns it with a valid
// session cookie, so a test can prove the EN-6 gate: a pending/suspended host's session is
// rejected (403) even though the JWT itself verifies.
func (a *apiHarness) hostWithStatus(t *testing.T, sub, status string) (*store.Host, *http.Cookie) {
	t.Helper()
	h, err := a.store.CreateHost(context.Background(), store.CreateHostParams{
		GoogleSub: sub, Email: sub + "@example.com", Name: sub, Status: status,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	tok, err := a.ring.Issue(h.ID, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return h, &http.Cookie{Name: auth.SessionCookie, Value: tok}
}

// AC-1 / T-1: the dashboard requires an authenticated, active host. No cookie → 401; a
// pending or suspended host's session → 403 (EN-6, live status read).
func TestApp_DashboardRequiresActiveHost(t *testing.T) {
	a := newAPIHarness(t)

	if rec := a.req(t, http.MethodGet, "/app", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /app without cookie = %d, want 401", rec.Code)
	}

	_, pending := a.hostWithStatus(t, "pending-host", store.HostPending)
	if rec := a.req(t, http.MethodGet, "/app", "", pending); rec.Code != http.StatusForbidden {
		t.Fatalf("GET /app as pending host = %d, want 403", rec.Code)
	}

	_, suspended := a.hostWithStatus(t, "suspended-host", store.HostSuspended)
	if rec := a.req(t, http.MethodGet, "/app", "", suspended); rec.Code != http.StatusForbidden {
		t.Fatalf("GET /app as suspended host = %d, want 403", rec.Code)
	}
}

// AC-1 / T-1: an active host lands on a dashboard that lists their streams (title +
// status). A foreign host's streams never appear.
func TestApp_DashboardListsOwnStreams(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")

	aliceStreamID := a.createStream(t, alice, "Alice Morning Show")
	a.createStream(t, bob, "Bob Secret Stream")

	rec := a.req(t, http.MethodGet, "/app", "", alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Alice Morning Show") {
		t.Fatalf("dashboard missing own stream title; body:\n%s", body)
	}
	if strings.Contains(body, "Bob Secret Stream") {
		t.Fatalf("dashboard leaked another host's stream")
	}
	if !strings.Contains(body, `href="/app/streams/`+aliceStreamID+`"`) {
		t.Fatalf("dashboard missing working stream link; body:\n%s", body)
	}
	if strings.Contains(body, `/app/streams/`+aliceStreamID+`/invites`) {
		t.Fatalf("dashboard still links to the removed /invites route; body:\n%s", body)
	}
}

// AC-1 / T-1: create a stream from the server-rendered form. POST → 303 back to the
// dashboard; the stream persists with the submitted title, schedule, and duration.
func TestApp_CreateStreamFromForm(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")

	form := url.Values{
		"title":        {"Launch Day"},
		"scheduled_at": {"2026-07-01T15:30"},
		"duration_min": {"90"},
	}
	rec := a.formReq(t, http.MethodPost, "/app/streams", form, alice)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /app/streams = %d, want 303; body %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/app" {
		t.Fatalf("redirect Location = %q, want /app", loc)
	}

	streams, err := a.store.ListStreamsByHost(context.Background(), host.ID)
	if err != nil {
		t.Fatalf("ListStreamsByHost: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("want 1 stream, got %d", len(streams))
	}
	s := streams[0]
	if s.Title != "Launch Day" {
		t.Fatalf("title = %q, want Launch Day", s.Title)
	}
	if s.ScheduledAt == nil || *s.ScheduledAt != time.Date(2026, 7, 1, 15, 30, 0, 0, time.UTC).Unix() {
		t.Fatalf("scheduled_at = %v, want the parsed UTC instant", s.ScheduledAt)
	}
	if s.DurationMin == nil || *s.DurationMin != 90 {
		t.Fatalf("duration_min = %v, want 90", s.DurationMin)
	}
}

// AC-1: a blank title is rejected (the form re-renders with a 400, never an empty-title
// stream).
func TestApp_CreateStreamBlankTitleRejected(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")

	rec := a.formReq(t, http.MethodPost, "/app/streams", url.Values{"title": {"   "}}, alice)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank title = %d, want 400", rec.Code)
	}
	streams, _ := a.store.ListStreamsByHost(context.Background(), host.ID)
	if len(streams) != 0 {
		t.Fatalf("a blank-title stream was persisted: %d", len(streams))
	}
}

// AC-1 / T-1: edit round-trips — the edit form prefills the current title, and a POST
// updates the stored stream.
func TestApp_EditStreamFromForm(t *testing.T) {
	a := newAPIHarness(t)
	host, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Working Title")

	edit := a.req(t, http.MethodGet, "/app/streams/"+id+"/edit", "", alice)
	if edit.Code != http.StatusOK {
		t.Fatalf("GET edit form = %d", edit.Code)
	}
	if !strings.Contains(edit.Body.String(), "Working Title") {
		t.Fatalf("edit form did not prefill the title; body:\n%s", edit.Body.String())
	}

	form := url.Values{"title": {"Final Title"}, "duration_min": {"45"}}
	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id, form, alice)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST update = %d, want 303; body %s", rec.Code, rec.Body.String())
	}

	got, err := a.store.GetStream(context.Background(), id)
	if err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	if got.Title != "Final Title" {
		t.Fatalf("title = %q, want Final Title", got.Title)
	}
	if got.DurationMin == nil || *got.DurationMin != 45 {
		t.Fatalf("duration = %v, want 45", got.DurationMin)
	}
	_ = host
}

// AC-1 / T-1: delete removes the stream and redirects to the dashboard.
func TestApp_DeleteStreamFromForm(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Throwaway")

	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/delete", nil, alice)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d, want 303", rec.Code)
	}
	if _, err := a.store.GetStream(context.Background(), id); err == nil {
		t.Fatalf("stream still exists after delete")
	}
}

// EN-6 / RF-2 isolation: a host cannot edit or delete another host's stream — both answer
// 404 so foreign ids can't be probed (mirrors the JSON API's ownedStream).
func TestApp_ForeignStreamMutationIs404(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	id := a.createStream(t, alice, "Alice Only")

	if rec := a.req(t, http.MethodGet, "/app/streams/"+id+"/edit", "", bob); rec.Code != http.StatusNotFound {
		t.Fatalf("bob GET alice's edit form = %d, want 404", rec.Code)
	}
	if rec := a.formReq(t, http.MethodPost, "/app/streams/"+id, url.Values{"title": {"hijacked"}}, bob); rec.Code != http.StatusNotFound {
		t.Fatalf("bob POST update alice's stream = %d, want 404", rec.Code)
	}
	if rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/delete", nil, bob); rec.Code != http.StatusNotFound {
		t.Fatalf("bob POST delete alice's stream = %d, want 404", rec.Code)
	}
	// Alice's stream is untouched.
	if got, err := a.store.GetStream(context.Background(), id); err != nil || got.Title != "Alice Only" {
		t.Fatalf("alice's stream was modified: %+v err %v", got, err)
	}
}
