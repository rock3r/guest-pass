package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// apiServer holds the dependencies of the host JSON API and the guest magic-link page.
type apiServer struct {
	store     *store.Store
	hasher    *token.Hasher
	mailer    mail.Mailer
	baseURL   string
	rd        *renderer
	hub       *signaling.Hub      // live slot (re)bind re-route (D-20); may be nil (minimal config)
	binds     *bindingLocks       // serialize a host's slot-binding ops with the /ws join-replay (D-20)
	auth      *auth.Authenticator // clears the session cookie on account erasure (DELETE /api/me, AC-5)
	liveCheck LiveChecker         // D-29 live-verify (watch link + verified status); may be nil
	trust     auth.TrustPolicy    // D-36 progressive-trust invite/stream quotas; zero value = disabled
}

// --- response DTOs (never expose token hashes or raw tokens) ---

type streamView struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	ScheduledAt *int64 `json:"scheduled_at,omitempty"`
	DurationMin *int64 `json:"duration_min,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func toStreamView(s *store.Stream) streamView {
	return streamView{ID: s.ID, Title: s.Title, Status: s.Status, ScheduledAt: s.ScheduledAt, DurationMin: s.DurationMin, CreatedAt: s.CreatedAt}
}

type passView struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CanScreen bool   `json:"can_screen"`
}

func toPassView(p *store.Pass) passView {
	v := passView{ID: p.ID, Role: p.Role, Status: p.Status, CanScreen: p.CanScreen}
	if p.Name != nil {
		v.Name = *p.Name
	}
	if p.Email != nil {
		v.Email = *p.Email
	}
	return v
}

// --- stream handlers (behind RequireHost) ---

type createStreamRequest struct {
	Title       string `json:"title"`
	ScheduledAt *int64 `json:"scheduled_at"`
	DurationMin *int64 `json:"duration_min"`
}

func (a *apiServer) createStream(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req createStreamRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if over, limit := overStreamQuota(r.Context(), a.store, a.trust, host, time.Now()); over {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("stream limit reached (%d) for your account", limit))
		return
	}
	s, err := a.store.CreateStream(r.Context(), store.CreateStreamParams{
		HostID: host.ID, Title: req.Title, ScheduledAt: req.ScheduledAt, DurationMin: req.DurationMin,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create stream")
		return
	}
	writeJSON(w, http.StatusCreated, toStreamView(s))
}

func (a *apiServer) listStreams(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	streams, err := a.store.ListStreamsByHost(r.Context(), host.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list streams")
		return
	}
	views := make([]streamView, 0, len(streams))
	for _, s := range streams {
		views = append(views, toStreamView(s))
	}
	writeJSON(w, http.StatusOK, views)
}

func (a *apiServer) getStream(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownedStream(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toStreamView(s))
}

func (a *apiServer) deleteStream(w http.ResponseWriter, r *http.Request) {
	s, ok := a.ownedStream(w, r)
	if !ok {
		return
	}
	// Deleting the LIVE stream tears down its room too (D-40): the FK cascade drops the sessions
	// row, but the host-scoped room + connected peers would otherwise linger into the host's next
	// stream. Hold the per-host binding lock across the liveness check, delete, and teardown so a
	// concurrent goLive can't interleave (codex); capture liveness BEFORE the delete.
	host, _ := auth.HostFromContext(r.Context())
	if host != nil {
		unlock := a.binds.lock(host.ID)
		defer unlock()
	}
	wasLive := host != nil && a.streamIsLive(r.Context(), host.ID, s.ID)
	peers := streamPeerIDs(r.Context(), a.store, s.ID) // collect BEFORE the cascade erases the passes
	if err := a.store.DeleteStream(r.Context(), s.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete stream")
		return
	}
	if host != nil {
		teardownDeletedStream(a.hub, host.ID, wasLive, peers)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownedStream loads the stream named by the {id} URL param and confirms it belongs to
// the authenticated host, returning 404 otherwise so a host can't probe others' ids.
func (a *apiServer) ownedStream(w http.ResponseWriter, r *http.Request) (*store.Stream, bool) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	s, err := a.store.GetStream(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && s.HostID != host.ID) {
		// Missing and foreign streams both answer 404 so a host can't probe others' ids.
		writeError(w, http.StatusNotFound, "stream not found")
		return nil, false
	}
	if err != nil {
		// A transient DB/scan error is not a 404 — surface it so the host doesn't act on
		// a healthy-looking "not found" while the backend is unhealthy.
		writeError(w, http.StatusInternalServerError, "could not load stream")
		return nil, false
	}
	return s, true
}

// --- pass handlers ---

type createPassRequest struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	CanScreen bool   `json:"can_screen"`
	ExpiresAt *int64 `json:"expires_at"`
}

func (a *apiServer) createPass(w http.ResponseWriter, r *http.Request) {
	stream, ok := a.ownedStream(w, r)
	if !ok {
		return
	}
	var req createPassRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Email) == "" {
		writeError(w, http.StatusBadRequest, "email is required to send an invite")
		return
	}
	// Progressive-trust invite cap (D-36): reject before minting/sending if the host is over quota.
	if host, ok := auth.HostFromContext(r.Context()); ok {
		if over, limit := overInviteQuota(r.Context(), a.store, a.trust, host, time.Now()); over {
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("invite limit reached (%d) for your account", limit))
			return
		}
	}
	role := store.RoleGuest
	if req.Role == store.RoleCohost {
		role = store.RoleCohost
	}

	raw, err := token.Mint()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint token")
		return
	}
	email := req.Email
	var namePtr *string
	if req.Name != "" {
		namePtr = &req.Name
	}
	pass, err := a.store.CreatePass(r.Context(), store.CreatePassParams{
		StreamID: stream.ID, Name: namePtr, Email: &email, Role: role,
		TokenHash: a.hasher.Hash(raw), CanScreen: req.CanScreen, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create pass")
		return
	}

	link := a.baseURL + "/p/" + raw
	if err := a.mailer.SendInvite(r.Context(), mail.Invite{To: email, GuestName: req.Name, StreamTitle: stream.Title, MagicLink: link}); err != nil {
		// The pass row exists; surface the delivery failure so the host can resend
		// (resend lands in M4). Do not echo the raw token/link in the error.
		writeError(w, http.StatusBadGateway, "pass created but invite delivery failed")
		return
	}
	if err := a.store.SetPassStatus(r.Context(), pass.ID, store.PassSent); err != nil {
		// The invite was delivered but we couldn't record it. Surface the failure rather
		// than returning a misleading "created" status with no sent_at, which would invite
		// a duplicate resend (M4).
		writeError(w, http.StatusInternalServerError, "invite sent but could not record pass status")
		return
	}
	pass.Status = store.PassSent
	writeJSON(w, http.StatusCreated, toPassView(pass))
}

func (a *apiServer) listPasses(w http.ResponseWriter, r *http.Request) {
	stream, ok := a.ownedStream(w, r)
	if !ok {
		return
	}
	passes, err := a.store.ListPassesByStream(r.Context(), stream.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list passes")
		return
	}
	views := make([]passView, 0, len(passes))
	for _, p := range passes {
		views = append(views, toPassView(p))
	}
	writeJSON(w, http.StatusOK, views)
}

// passLanding handles GET /p/{token}. It is SIDE-EFFECT-FREE (EN-10): it validates the
// token and renders the landing page but never transitions the pass to "opened" — that
// happens only on an explicit device-check entry (M2), so mail scanners/unfurlers that
// prefetch the link can't false-positive. Revoked/expired passes get a turned-off page.
func (a *apiServer) passLanding(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "token")
	pass, err := a.store.GetPassByTokenHash(r.Context(), a.hasher.Hash(raw))
	if errors.Is(err, store.ErrNotFound) {
		// Unknown or rotated token. Constant-time compare upstream means no timing oracle.
		http.Error(w, "pass not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load pass")
		return
	}
	if passRetired(pass) {
		http.Error(w, "this invite link has been turned off", http.StatusGone)
		return
	}
	stream, err := a.store.GetStream(r.Context(), pass.StreamID)
	title, watchURL := "", ""
	if err == nil {
		title = stream.Title
		// Guest "watch live" link (D-29/AC-8): the public link for the stream's linked channel, if
		// any. Pure (no live check) — the link works regardless of current live status.
		if a.liveCheck != nil {
			watchURL = a.streamWatchURL(stream.TwitchYTPlatform, stream.TwitchYTChannel)
		}
	}
	guest := ""
	if pass.Name != nil {
		guest = *pass.Name
	}
	a.rd.passLandingPage(w, r, title, guest, watchURL, raw)
}

// passEnter handles POST /p/{token}/enter — the explicit device-check entry. It is the ONE
// place a magic-link pass transitions to "opened" (EN-10): GET is side-effect-free, this
// POST does the transition. The transition fires only from a pre-opened state (created /
// sent), so a repeat entry is idempotent — it never re-stamps opened_at nor regresses an
// already-opened/accepted pass. Authenticated by the pass token alone (no host session).
func (a *apiServer) passEnter(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "token")
	pass, err := a.store.GetPassByTokenHash(r.Context(), a.hasher.Hash(raw))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "pass not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load pass")
		return
	}
	if passRetired(pass) {
		http.Error(w, "this invite link has been turned off", http.StatusGone)
		return
	}
	// EN-6 parity with the WS join: the pass's host must be active right now. A
	// suspended/pending host's guests can't enter — kept opaque to the guest (same
	// "turned off" screen, never leaking the host's status).
	if !a.hostActive(r.Context(), pass.StreamID) {
		http.Error(w, "this invite link has been turned off", http.StatusGone)
		return
	}
	// Atomic exactly-once transition (EN-10): MarkPassOpened only fires from created/sent and
	// only while unexpired, so concurrent or repeated entries can't re-stamp opened_at nor
	// regress an already-opened/accepted pass — no read-then-write race.
	opened, err := a.store.MarkPassOpened(r.Context(), pass.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not update pass")
		return
	}
	if !opened {
		// No transition: either a valid repeat entry (already opened/accepted), or the pass
		// became retired/deleted in the gap since the pre-check. Re-read and reject anything
		// that is no longer a usable pass rather than acknowledging success for it.
		cur, err := a.store.GetPass(r.Context(), pass.ID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && passRetired(cur)) {
			http.Error(w, "this invite link has been turned off", http.StatusGone)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// hostActive reports whether the stream's host is currently active (EN-6). A lookup
// failure is treated as not-active so a transient error never lets a guest enter.
func (a *apiServer) hostActive(ctx context.Context, streamID string) bool {
	stream, err := a.store.GetStream(ctx, streamID)
	if err != nil {
		return false
	}
	host, err := a.store.GetHost(ctx, stream.HostID)
	if err != nil {
		return false
	}
	return host.Status == store.HostActive
}

// passRetired reports whether a magic-link pass can no longer be used by a guest: revoked,
// expired by status, or past its expiry deadline. Such a pass shows the "link turned off"
// screen and can never transition to opened (kept consistent with the WS join check).
func passRetired(p *store.Pass) bool {
	if p.Status == store.PassRevoked || p.Status == store.PassExpired {
		return true
	}
	return p.ExpiresAt != nil && time.Now().Unix() >= *p.ExpiresAt
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes the request body into v, rejecting unknown fields and oversized
// bodies. It writes a 400 and returns false on failure.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
