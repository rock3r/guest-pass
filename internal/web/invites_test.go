package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	i := strings.LastIndex(link, "/p/")
	if i < 0 {
		t.Fatalf("no /p/ segment in magic link %q", link)
	}
	return link[i+len("/p/"):]
}

// firstPass returns the single pass of a stream (fails if not exactly one).
func (a *apiHarness) onlyPass(t *testing.T, streamID string) *store.Pass {
	t.Helper()
	passes, err := a.store.ListPassesByStream(context.Background(), streamID)
	if err != nil {
		t.Fatalf("ListPassesByStream: %v", err)
	}
	if len(passes) != 1 {
		t.Fatalf("want exactly 1 pass, got %d", len(passes))
	}
	return passes[0]
}

// AC-3 / EN-23: the Invites tab exposes an invite form of name/email/role ONLY — never the
// live production controls (slot binding, screenshare eligibility, mic/cam), which live in
// the greenroom.
func TestApp_StreamDetailInviteFormFieldsOnly(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "My Show")

	rec := a.req(t, http.MethodGet, "/app/streams/"+id, "", alice)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET detail = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`name="name"`, `name="email"`, `name="role"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("invite form missing %s; body:\n%s", want, body)
		}
	}
	// EN-23: no production controls on the invites tab.
	for _, forbidden := range []string{`name="can_screen"`, `name="slot"`, `name="slot_id"`, `name="screenshare"`, `name="mic"`, `name="cam"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("invites tab leaked a production control %s (EN-23)", forbidden)
		}
	}
}

// AC-3: creating an invite from the form mints a pass (status sent), emails the magic link,
// and reveals the link once in the response (the raw token is never stored).
func TestApp_CreateInviteFromForm(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Launch")

	form := url.Values{"name": {"Dana"}, "email": {"dana@example.com"}, "role": {"cohost"}}
	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes", form, alice)
	// POST-redirect-GET: the create POST redirects to the detail page with a one-time
	// reveal nonce (so a refresh can't re-mint the pass).
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create invite = %d, want 303; body %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/app/streams/"+id+"?reveal=") {
		t.Fatalf("create redirect Location = %q, want a reveal of the detail page", loc)
	}

	inv := a.mailer.lastInvite(t)
	if inv.To != "dana@example.com" {
		t.Fatalf("invite To = %q, want dana@example.com", inv.To)
	}
	// Following the redirect reveals the link once; the link is never in the URL itself.
	if strings.Contains(loc, inv.MagicLink) || strings.Contains(loc, "/p/") {
		t.Fatalf("reveal redirect URL leaked the token: %q", loc)
	}
	reveal := a.req(t, http.MethodGet, loc, "", alice)
	if reveal.Code != http.StatusOK || !strings.Contains(reveal.Body.String(), inv.MagicLink) {
		t.Fatalf("reveal page did not show the magic link once (code %d)", reveal.Code)
	}

	pass := a.onlyPass(t, id)
	if pass.Status != store.PassSent {
		t.Fatalf("pass status = %q, want sent", pass.Status)
	}
	if pass.Role != store.RoleCohost {
		t.Fatalf("pass role = %q, want cohost", pass.Role)
	}
	// The revealed link resolves to this pass (token stored hashed, EN-5).
	got, err := a.store.GetPassByTokenHash(context.Background(), a.hasher.Hash(tokenFromLink(t, inv.MagicLink)))
	if err != nil || got.ID != pass.ID {
		t.Fatalf("revealed link does not resolve to the pass: %v", err)
	}
}

// AC-3: a blank email is rejected (no pass minted).
func TestApp_CreateInviteRequiresEmail(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Launch")

	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes", url.Values{"email": {"  "}}, alice)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank email = %d, want 400", rec.Code)
	}
	if passes, _ := a.store.ListPassesByStream(context.Background(), id); len(passes) != 0 {
		t.Fatalf("a pass was minted for a blank email")
	}
}

// AC-3 (D-15): a pass's role is inline-editable Guest↔Co-host from the invites tab.
func TestApp_InviteRoleInlineEdit(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")
	a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes", url.Values{"email": {"g@example.com"}, "role": {"guest"}}, alice)
	pass := a.onlyPass(t, id)

	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes/"+pass.ID+"/role", url.Values{"role": {"cohost"}}, alice)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("role edit = %d, want 303", rec.Code)
	}
	if got, _ := a.store.GetPass(context.Background(), pass.ID); got.Role != store.RoleCohost {
		t.Fatalf("role = %q, want cohost", got.Role)
	}
	// Demote back to guest.
	a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes/"+pass.ID+"/role", url.Values{"role": {"guest"}}, alice)
	if got, _ := a.store.GetPass(context.Background(), pass.ID); got.Role != store.RoleGuest {
		t.Fatalf("role = %q, want guest after demote", got.Role)
	}
	// An invalid role is rejected.
	if rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes/"+pass.ID+"/role", url.Values{"role": {"admin"}}, alice); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid role = %d, want 400", rec.Code)
	}
}

// AC-3 / PD-2: re-issuing rotates the magic-link token (the old link stops resolving),
// re-emails a fresh link, and sets the pass back to sent.
func TestApp_ReissueRotatesToken(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")
	a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes", url.Values{"email": {"g@example.com"}}, alice)
	pass := a.onlyPass(t, id)
	oldToken := tokenFromLink(t, a.mailer.lastInvite(t).MagicLink)

	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes/"+pass.ID+"/reissue", nil, alice)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reissue = %d, want 303; body %s", rec.Code, rec.Body.String())
	}
	newToken := tokenFromLink(t, a.mailer.lastInvite(t).MagicLink)
	if newToken == oldToken {
		t.Fatal("reissue did not rotate the token")
	}
	// Old token no longer resolves; new token resolves to the same pass.
	if _, err := a.store.GetPassByTokenHash(context.Background(), a.hasher.Hash(oldToken)); err == nil {
		t.Fatal("old token still resolves after reissue")
	}
	got, err := a.store.GetPassByTokenHash(context.Background(), a.hasher.Hash(newToken))
	if err != nil || got.ID != pass.ID {
		t.Fatalf("new token does not resolve to the pass: %v", err)
	}
	if got.Status != store.PassSent {
		t.Fatalf("reissued pass status = %q, want sent", got.Status)
	}
}

// AC-3 / PD-2: revoke turns the pass off (status revoked).
func TestApp_RevokePass(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")
	a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes", url.Values{"email": {"g@example.com"}}, alice)
	pass := a.onlyPass(t, id)

	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes/"+pass.ID+"/revoke", nil, alice)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke = %d, want 303", rec.Code)
	}
	if got, _ := a.store.GetPass(context.Background(), pass.ID); got.Status != store.PassRevoked {
		t.Fatalf("status = %q, want revoked", got.Status)
	}
}

// The reveal is one-time (Cursor Bugbot, M4 PR-3): re-loading the same reveal URL no longer
// shows the link, so a refresh/back never re-mints or re-reveals a stale link.
func TestApp_RevealIsOneTime(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	id := a.createStream(t, alice, "Show")

	rec := a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes", url.Values{"email": {"g@example.com"}}, alice)
	loc := rec.Header().Get("Location")
	link := a.mailer.lastInvite(t).MagicLink

	first := a.req(t, http.MethodGet, loc, "", alice)
	if !strings.Contains(first.Body.String(), link) {
		t.Fatalf("first reveal load did not show the link")
	}
	second := a.req(t, http.MethodGet, loc, "", alice)
	if strings.Contains(second.Body.String(), link) {
		t.Fatalf("reveal is not one-time: the link showed again on reload")
	}
}

// EN-6 isolation: a foreign host cannot view or mutate another host's stream/passes.
func TestApp_DetailOwnershipIsolation(t *testing.T) {
	a := newAPIHarness(t)
	_, alice := a.host(t, "alice")
	_, bob := a.host(t, "bob")
	id := a.createStream(t, alice, "Alice Only")
	a.formReq(t, http.MethodPost, "/app/streams/"+id+"/passes", url.Values{"email": {"g@example.com"}}, alice)
	pass := a.onlyPass(t, id)

	cases := []struct{ method, target string }{
		{http.MethodGet, "/app/streams/" + id},
		{http.MethodPost, "/app/streams/" + id + "/passes"},
		{http.MethodPost, "/app/streams/" + id + "/passes/" + pass.ID + "/role"},
		{http.MethodPost, "/app/streams/" + id + "/passes/" + pass.ID + "/reissue"},
		{http.MethodPost, "/app/streams/" + id + "/passes/" + pass.ID + "/revoke"},
	}
	for _, c := range cases {
		rec := a.formReq(t, c.method, c.target, url.Values{"email": {"x@example.com"}, "role": {"guest"}}, bob)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as non-owner = %d, want 404", c.method, c.target, rec.Code)
		}
	}
	// Alice's pass is untouched.
	if got, _ := a.store.GetPass(context.Background(), pass.ID); got.Status == store.PassRevoked {
		t.Fatal("foreign revoke modified the pass")
	}
}
