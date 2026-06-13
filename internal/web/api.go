package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// apiServer holds the dependencies of the host JSON API and the guest magic-link page.
type apiServer struct {
	store   *store.Store
	hasher  *token.Hasher
	mailer  mail.Mailer
	baseURL string
	rd      *renderer
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
	if err := a.store.DeleteStream(r.Context(), s.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete stream")
		return
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
	if err != nil || s.HostID != host.ID {
		writeError(w, http.StatusNotFound, "stream not found")
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
	if err := a.store.SetPassStatus(r.Context(), pass.ID, store.PassSent); err == nil {
		pass.Status = store.PassSent
	}
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
	if pass.Status == store.PassRevoked || pass.Status == store.PassExpired {
		http.Error(w, "this invite link has been turned off", http.StatusGone)
		return
	}
	stream, err := a.store.GetStream(r.Context(), pass.StreamID)
	title := ""
	if err == nil {
		title = stream.Title
	}
	guest := ""
	if pass.Name != nil {
		guest = *pass.Name
	}
	a.rd.passLandingPage(w, r, title, guest)
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
