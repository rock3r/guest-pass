package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/store"
)

// maxReportMessage caps the free-text length of an abuse report defensively (the form also sets
// maxlength); truncation is rune-safe so we never split a UTF-8 sequence.
const maxReportMessage = 2000

// reportForm renders the public "report this invite" form (D-42 / AC-11). No auth: the magic-link
// token in the path is the only identifier. An unknown token is 404 (same opaque response as the
// pass page); a known token — even a retired one — can still be reported.
func (a *apiServer) reportForm(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "token")
	if _, err := a.store.GetPassByTokenHash(r.Context(), a.hasher.Hash(raw)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "pass not found", http.StatusNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load pass")
		return
	}
	q := r.URL.Query()
	a.rd.reportPage(w, r, raw, q.Get("sent") == "1", q.Get("error") == "1")
}

// reportSubmit records an abuse report (D-42 / EN-24). category + message are the only form inputs,
// both required. The reported host, the stream, and the reporter's email are ALL resolved
// server-side from the token (never form input) so a reporter can't forge whom they are or who they
// report. Public + rate-limited (same per-IP limiter as the pass page). PRG to a thank-you state.
func (a *apiServer) reportSubmit(w http.ResponseWriter, r *http.Request) {
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

	category := strings.TrimSpace(r.FormValue("category"))
	message := strings.TrimSpace(r.FormValue("message"))
	if !store.ValidReportCategory(category) || message == "" {
		http.Redirect(w, r, "/p/"+raw+"/report?error=1", http.StatusSeeOther)
		return
	}
	if runes := []rune(message); len(runes) > maxReportMessage {
		message = string(runes[:maxReportMessage])
	}

	// Resolve the reported host + stream from the token (EN-24). A resolvable pass's stream always
	// exists (the pass FK-cascades with it); a miss here means a concurrent delete — treat as 404.
	stream, err := a.store.GetStream(r.Context(), pass.StreamID)
	if err != nil {
		http.Error(w, "pass not found", http.StatusNotFound)
		return
	}
	sid := stream.ID
	if _, err := a.store.CreateReport(r.Context(), store.CreateReportParams{
		HostID: stream.HostID, StreamID: &sid, ReporterEmail: pass.Email,
		Category: category, Message: message,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "could not submit report")
		return
	}
	http.Redirect(w, r, "/p/"+raw+"/report?sent=1", http.StatusSeeOther)
}
