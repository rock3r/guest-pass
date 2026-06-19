package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// detailData backs the tabbed stream-detail page (Invites + read-only Sources, EN-26).
// Live production controls (slot binding, screenshare eligibility, mic/cam) are NEVER on
// this page — they live in the host-only greenroom People controls (EN-23). Tab names the
// active section so the bar can highlight it.
type detailData struct {
	StreamID    string
	StreamTitle string
	Tab         string // "invites" | "sources"
	// Live-session state (EN-2/D-20): SessionLive is true when THIS stream is the host's active
	// session; OtherStreamLive is true when the host is live for a DIFFERENT stream (so the page
	// explains the one-live-at-a-time rule instead of offering a "Go live" that would 409).
	SessionLive     bool
	OtherStreamLive bool
	// Invites tab
	Passes []passRow
	Issued *issuedLink // set after create/reissue to reveal the fresh link once
	// Live verification (D-29/AC-8): the optional linked channel + its public watch link, plus the
	// PRG flash flags from the link/unlink form.
	LinkedPlatform string // "twitch" when a channel is linked, else ""
	LinkedChannel  string // the linked channel id, else ""
	WatchURL       string // public watch-live link for the linked channel, else ""
	ChannelLinked  bool   // ?channel=linked
	ChannelCleared bool   // ?channel=cleared
	ChannelError   bool   // ?error=channel — the submitted channel was invalid
	// Progressive-trust (D-36)
	InviteQuotaError bool // ?error=invite-quota — the host hit their per-window invite cap
	// Sources tab (read-only, EN-26)
	Slots     []slotCard
	RevealAll string // newline-joined freshly-minted OBS URLs for the copy-all block ("" if none)
	HasReveal bool
}

// passRow is a guest pass as the invites table renders it (display-ready).
type passRow struct {
	ID       string
	Name     string
	Email    string
	Role     string // "guest" | "cohost"
	IsCohost bool
	Status   string // display label (PD-1: "Accepted", etc.)
	Expiry   string // formatted absolute UTC, or "" when none
	Retired  bool   // revoked or expired — revoke is disabled, re-issue still offered
}

// issuedLink is the freshly-minted magic link revealed ONCE after create/re-issue. The raw
// token is never stored (EN-5), so it can only be surfaced here, at mint time. Delivered is
// false when the invite email bounced — the host can then share the link manually.
type issuedLink struct {
	Email     string
	Link      string
	Delivered bool
}

// streamDetail renders the tabbed stream-detail page (Invites tab) (AC-3). A `?reveal=`
// nonce (set by the create/re-issue redirect) surfaces the freshly-minted magic link once.
func (s *appServer) streamDetail(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	var issued *issuedLink
	if v, ok := s.reveals.take(r.URL.Query().Get("reveal"), time.Now()); ok {
		if il, ok := v.(issuedLink); ok {
			issued = &il
		}
	}
	s.renderDetail(w, r, host, st, issued)
}

// redirectWithReveal stores il for a one-time reveal and 303-redirects to the detail page
// with the nonce (POST-redirect-GET). The nonce is not the token, so nothing secret rides
// the URL. If the reveal can't be stored, the host is still redirected (the invite was
// emailed); they can re-issue to see a link.
func (s *appServer) redirectWithReveal(w http.ResponseWriter, r *http.Request, streamID string, il issuedLink) {
	dest := "/app/streams/" + streamID
	if nonce, err := s.reveals.put(il, time.Now()); err == nil {
		dest += "?reveal=" + nonce
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// renderDetail loads the stream's passes and renders the detail page, optionally revealing
// a just-issued link (after create/re-issue).
func (s *appServer) renderDetail(w http.ResponseWriter, r *http.Request, host *store.Host, st *store.Stream, issued *issuedLink) {
	passes, err := s.store.ListPassesByStream(r.Context(), st.ID)
	if err != nil {
		http.Error(w, "could not load invites", http.StatusInternalServerError)
		return
	}
	now := time.Now().Unix()
	rows := make([]passRow, 0, len(passes))
	for _, p := range passes {
		rows = append(rows, toPassRow(p, now))
	}
	live, otherLive := s.sessionState(r.Context(), host.ID, st.ID)
	d := detailData{
		StreamID: st.ID, StreamTitle: st.Title, Tab: "invites", Passes: rows, Issued: issued,
		SessionLive: live, OtherStreamLive: otherLive,
	}
	// Live verification (D-29/AC-8): surface the linked channel + watch link + the form's PRG flash.
	if st.TwitchYTPlatform != nil && st.TwitchYTChannel != nil {
		d.LinkedPlatform, d.LinkedChannel = *st.TwitchYTPlatform, *st.TwitchYTChannel
		if s.liveCheck != nil {
			d.WatchURL = s.liveCheck.WatchURL(*st.TwitchYTPlatform, *st.TwitchYTChannel)
		}
	}
	q := r.URL.Query()
	d.ChannelLinked = q.Get("channel") == "linked"
	d.ChannelCleared = q.Get("channel") == "cleared"
	d.ChannelError = q.Get("error") == "channel"
	d.InviteQuotaError = q.Get("error") == "invite-quota"
	s.rd.render(w, r, "streamdetail.html", pageData{
		Title: st.Title, Nav: "dashboard", Host: &navHost{Name: host.Name, IsAdmin: host.IsAdmin},
		Data: d,
	})
}

// createInvite mints a guest pass from the invite form (name/email/role only — EN-23),
// emails the magic link, and reveals it once. Eligibility (can_screen) is never set here.
func (s *appServer) createInvite(w http.ResponseWriter, r *http.Request) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	email := strings.TrimSpace(r.PostFormValue("email"))
	if email == "" {
		http.Error(w, "email is required to send an invite", http.StatusBadRequest)
		return
	}
	// Progressive-trust invite cap (D-36): reject before minting/sending if the host has hit their
	// per-window invite quota. PRG back to the stream detail with the quota flash.
	if over, _ := overInviteQuota(r.Context(), s.store, s.trust, host, time.Now()); over {
		http.Redirect(w, r, "/app/streams/"+st.ID+"?error=invite-quota", http.StatusSeeOther)
		return
	}
	role := normalizeRole(r.PostFormValue("role"))
	link, delivered, err := s.mintInvite(r.Context(), st, name, email, role)
	if err != nil {
		http.Error(w, "could not create invite", http.StatusInternalServerError)
		return
	}
	s.redirectWithReveal(w, r, st.ID, issuedLink{Email: email, Link: link, Delivered: delivered})
}

// setInviteRole flips a pass's role guest↔cohost (D-15), then redirects to the detail page.
func (s *appServer) setInviteRole(w http.ResponseWriter, r *http.Request) {
	_, st, pass, ok := s.ownedStreamAndPass(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	role := r.PostFormValue("role")
	if role != store.RoleGuest && role != store.RoleCohost {
		http.Error(w, "invalid role", http.StatusBadRequest)
		return
	}
	if err := s.store.SetPassRole(r.Context(), pass.ID, role); err != nil {
		http.Error(w, "could not update role", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/streams/"+st.ID, http.StatusSeeOther)
}

// reissueInvite rotates the pass's magic-link token (the old link stops resolving),
// re-emails it, sets the pass back to sent (PD-2), and reveals the new link once.
func (s *appServer) reissueInvite(w http.ResponseWriter, r *http.Request) {
	host, st, pass, ok := s.ownedStreamAndPass(w, r)
	if !ok {
		return
	}
	// Re-issue re-sends a real invite email and re-stamps sent_at, so it counts against — and is
	// capped by — the progressive-trust invite quota (D-36) exactly like a fresh invite. Without this
	// gate a host could spam unbounded emails to one address by repeatedly re-issuing a single pass.
	if over, _ := overInviteQuota(r.Context(), s.store, s.trust, host, time.Now()); over {
		http.Redirect(w, r, "/app/streams/"+st.ID+"?error=invite-quota", http.StatusSeeOther)
		return
	}
	raw, err := token.Mint()
	if err != nil {
		http.Error(w, "could not mint token", http.StatusInternalServerError)
		return
	}
	// Clear the binding (ReissuePass nulls slot_id) AND vacate any LIVE slot the guest held, under
	// the per-host binding lock so it orders with the /ws join-replay + picker PUTs (D-20). A
	// re-issued invite starts unbound — the host re-binds in the greenroom if needed (codex).
	unlock := s.binds.lock(st.HostID)
	if err := s.store.ReissuePass(r.Context(), pass.ID, s.hasher.Hash(raw)); err != nil {
		unlock()
		http.Error(w, "could not re-issue invite", http.StatusInternalServerError)
		return
	}
	if s.hub != nil {
		if room := s.hub.RoomIfLive(st.HostID); room != nil {
			room.VacateOccupant(signaling.PeerID(pass.ID))
		}
	}
	unlock()
	name, email := passNameEmail(pass)
	link := s.baseURL + "/p/" + raw
	delivered := s.deliverInvite(r.Context(), st, name, email, link) == nil
	s.redirectWithReveal(w, r, st.ID, issuedLink{Email: email, Link: link, Delivered: delivered})
}

// revokeInvite turns a pass off (status revoked → the guest's link shows "link turned off",
// PD-2), then redirects to the detail page.
func (s *appServer) revokeInvite(w http.ResponseWriter, r *http.Request) {
	_, st, pass, ok := s.ownedStreamAndPass(w, r)
	if !ok {
		return
	}
	if err := s.store.SetPassStatus(r.Context(), pass.ID, store.PassRevoked); err != nil {
		http.Error(w, "could not revoke invite", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/streams/"+st.ID, http.StatusSeeOther)
}

// mintInvite creates a pass for st (token stored hashed, EN-5), emails the magic link, and
// marks it sent. It returns the link to reveal once and whether delivery succeeded; on a
// delivery failure the pass row still exists (status stays created) so the host can share
// the revealed link or re-issue. can_screen is never set at invite time (EN-23).
func (s *appServer) mintInvite(ctx context.Context, st *store.Stream, name, email, role string) (string, bool, error) {
	raw, err := token.Mint()
	if err != nil {
		return "", false, err
	}
	var namePtr *string
	if name != "" {
		namePtr = &name
	}
	em := email
	pass, err := s.store.CreatePass(ctx, store.CreatePassParams{
		StreamID: st.ID, Name: namePtr, Email: &em, Role: role, TokenHash: s.hasher.Hash(raw),
	})
	if err != nil {
		return "", false, err
	}
	link := s.baseURL + "/p/" + raw
	if s.deliverInvite(ctx, st, name, email, link) != nil {
		return link, false, nil // row exists; delivery failed — host can re-issue or share manually
	}
	// Delivery succeeded. Stamp "sent" best-effort: if this write fails the invite still went
	// out and the pass exists, so we reveal the link rather than failing the request and
	// hiding it — the pass simply stays "created" until a re-issue re-stamps it.
	_ = s.store.SetPassStatus(ctx, pass.ID, store.PassSent)
	return link, true, nil
}

func (s *appServer) deliverInvite(ctx context.Context, st *store.Stream, name, email, link string) error {
	return s.mailer.SendInvite(ctx, mail.Invite{To: email, GuestName: name, StreamTitle: st.Title, MagicLink: link})
}

// ownedStreamAndPass loads the {id} stream (owned by the host) and the {pid} pass, and
// confirms the pass belongs to that stream. Any mismatch answers 404 so ids can't be
// probed (mirrors ownedStream).
func (s *appServer) ownedStreamAndPass(w http.ResponseWriter, r *http.Request) (*store.Host, *store.Stream, *store.Pass, bool) {
	host, st, ok := s.ownedStream(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	pass, err := s.store.GetPass(r.Context(), chi.URLParam(r, "pid"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && pass.StreamID != st.ID) {
		http.Error(w, "invite not found", http.StatusNotFound)
		return nil, nil, nil, false
	}
	if err != nil {
		http.Error(w, "could not load invite", http.StatusInternalServerError)
		return nil, nil, nil, false
	}
	return host, st, pass, true
}

func toPassRow(p *store.Pass, now int64) passRow {
	name, email := passNameEmail(p)
	return passRow{
		ID:       p.ID,
		Name:     name,
		Email:    email,
		Role:     p.Role,
		IsCohost: p.Role == store.RoleCohost,
		Status:   passStatusLabel(p, now),
		Expiry:   formatSchedule(p.ExpiresAt),
		Retired:  p.Status == store.PassRevoked || passExpired(p, now),
	}
}

func passNameEmail(p *store.Pass) (name, email string) {
	if p.Name != nil {
		name = *p.Name
	}
	if p.Email != nil {
		email = *p.Email
	}
	return name, email
}

func passExpired(p *store.Pass, now int64) bool {
	if p.Status == store.PassExpired {
		return true
	}
	return p.ExpiresAt != nil && now >= *p.ExpiresAt
}

// passStatusLabel maps a pass to its host-facing status label (PD-1: "Accepted"; a pass
// past its deadline reads "Expired" even if its stored status hasn't been swept yet).
func passStatusLabel(p *store.Pass, now int64) string {
	switch {
	case p.Status == store.PassRevoked:
		return "Revoked"
	case p.Status == store.PassAccepted:
		return "Accepted"
	case passExpired(p, now):
		return "Expired"
	case p.Status == store.PassOpened:
		return "Opened"
	case p.Status == store.PassSent:
		return "Sent"
	default:
		return "Created"
	}
}

// normalizeRole coerces a submitted role to the allowed set, defaulting to guest.
func normalizeRole(v string) string {
	if v == store.RoleCohost {
		return store.RoleCohost
	}
	return store.RoleGuest
}
