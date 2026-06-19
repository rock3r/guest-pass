package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/rock3r/guest-pass/internal/livecheck"
)

// LiveChecker is the web layer's view of internal/livecheck (D-29 / AC-8): the pure WatchURL /
// Normalize helpers (build the guest watch link, validate the host's channel input — no network)
// plus the network Check (the verified-live status). *livecheck.Checker satisfies it; tests pass a
// fake so the live-status endpoint is exercised without a real Twitch fetch.
type LiveChecker interface {
	WatchURL(platform, channel string) string
	Normalize(platform, channel string) (string, bool)
	Check(ctx context.Context, platform, channel string) livecheck.Result
}

// setStreamChannel links / unlinks a stream's live-verify channel from the stream-detail form
// (POST /app/streams/{id}/channel, host-only, PRG; CSRF-safe via the SameSite cookie). An empty
// channel unlinks; a non-empty channel is validated via the checker's Normalize (Twitch-only in v1)
// and rejected (?error=channel) if invalid. Platform + channel are stored together (D-29).
func (s *appServer) setStreamChannel(w http.ResponseWriter, r *http.Request) {
	_, st, ok := s.ownedStream(w, r)
	if !ok {
		return
	}
	dest := "/app/streams/" + st.ID
	platform := strings.TrimSpace(r.FormValue("platform"))
	channel := strings.TrimSpace(r.FormValue("channel"))

	if channel == "" { // unlink
		if err := s.store.SetStreamChannel(r.Context(), st.ID, nil, nil); err != nil {
			http.Error(w, "could not update channel", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, dest+"?channel=cleared", http.StatusSeeOther)
		return
	}
	norm, valid := s.liveCheck.Normalize(platform, channel)
	if !valid {
		http.Redirect(w, r, dest+"?error=channel", http.StatusSeeOther)
		return
	}
	if err := s.store.SetStreamChannel(r.Context(), st.ID, &platform, &norm); err != nil {
		http.Error(w, "could not update channel", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, dest+"?channel=linked", http.StatusSeeOther)
}

// livecheckStatusView is the JSON shape of GET /api/streams/{id}/livecheck.
type livecheckStatusView struct {
	Linked   bool   `json:"linked"`
	Status   string `json:"status"` // live | offline | status-unavailable
	Platform string `json:"platform,omitempty"`
	Channel  string `json:"channel,omitempty"`
	WatchURL string `json:"watch_url,omitempty"`
}

// livecheckStatus serves GET /api/streams/{id}/livecheck (host-only): the verified-live status +
// watch link for the stream's linked channel — the D-24 broadcast-layer fold the greenroom polls to
// show "live (verified on <platform>)". With no channel linked it returns linked=false /
// status-unavailable and performs no fetch; a degraded check also returns status-unavailable. Never
// an error (best-effort, D-24).
func (a *apiServer) livecheckStatus(w http.ResponseWriter, r *http.Request) {
	st, ok := a.ownedStream(w, r)
	if !ok {
		return
	}
	if st.TwitchYTPlatform == nil || st.TwitchYTChannel == nil {
		writeJSON(w, http.StatusOK, livecheckStatusView{Linked: false, Status: string(livecheck.StatusUnavailable)})
		return
	}
	res := a.liveCheck.Check(r.Context(), *st.TwitchYTPlatform, *st.TwitchYTChannel)
	writeJSON(w, http.StatusOK, livecheckStatusView{
		Linked:   true,
		Status:   string(res.Status),
		Platform: *st.TwitchYTPlatform,
		Channel:  *st.TwitchYTChannel,
		WatchURL: res.WatchURL,
	})
}

// streamWatchURL returns the public watch-live link for a stream's linked channel, or "" if none —
// the guest-facing link surfaced on the pass page (D-29). Pure (no network).
func (a *apiServer) streamWatchURL(platform, channel *string) string {
	if platform == nil || channel == nil {
		return ""
	}
	return a.liveCheck.WatchURL(*platform, *channel)
}
