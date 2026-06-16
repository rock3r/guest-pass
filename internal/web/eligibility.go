package web

import (
	"net/http"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
)

// patchPassRequest updates a guest's live-managed pass attributes. Currently screenshare
// eligibility (can_screen, EN-23/AC-9). A POINTER so an omitted field is left unchanged rather than
// unmarshalling to false and silently revoking.
type patchPassRequest struct {
	CanScreen *bool `json:"canScreen"`
}

// patchPassResponse echoes the eligibility now in effect and whether the change also re-projected
// the LIVE room (true) or was persisted DB-only because the stream isn't the host's active session.
type patchPassResponse struct {
	CanScreen bool `json:"canScreen"`
	Live      bool `json:"live"`
}

// patchPass updates a guest's live-managed pass attributes — currently screenshare eligibility
// (can_screen, EN-23/AC-9). Host-only (RequireHost); RF-2 same-host via ownedPass. It persists
// passes.can_screen and, when the pass's stream is the host's live session, re-projects the roster
// so the guest's share affordance reflects it; a REVOKE additionally runs the force-no-share path
// (pull an active share, drop the live screenshare slot — no auto-advance). Serialized with the
// host's other pass ops via the binding lock, so the DB write and the live command keep order.
func (a *apiServer) patchPass(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	defer a.binds.lock(host.ID)()
	pass, _, ok := a.ownedPass(w, r)
	if !ok {
		return
	}
	var req patchPassRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.CanScreen == nil {
		writeError(w, http.StatusBadRequest, "canScreen is required")
		return
	}
	canScreen := *req.CanScreen
	if err := a.store.SetPassCanScreen(r.Context(), pass.ID, canScreen); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update screenshare eligibility")
		return
	}
	// Re-project whenever the guest is CONNECTED to the host's room — NOT gated on the active session
	// (unlike slot binding, which touches the on-air pool): a connected guest's share affordance must
	// reflect an eligibility change immediately, pre-live or live. The DB write above always persists;
	// a guest not currently connected picks it up via the join seed.
	live := a.liveScreenEligible(host.ID, pass.ID, canScreen)
	writeJSON(w, http.StatusOK, patchPassResponse{CanScreen: canScreen, Live: live})
}

// liveScreenEligible re-projects a guest's eligibility in the host's room (if one is up) and runs
// the force-no-share revoke side-effect (D-13/AC-9). Returns whether a room re-projected — false
// when no peers are connected (DB-only; the join seed carries the value when the guest connects). A
// re-project for a guest not in the room is a harmless no-op (the reducer no-ops an absent peer).
func (a *apiServer) liveScreenEligible(hostID, passID string, canScreen bool) bool {
	if a.hub == nil {
		return false
	}
	room := a.hub.RoomIfLive(hostID)
	if room == nil {
		return false
	}
	room.SetScreenEligibleLive(signaling.PeerID(passID), canScreen)
	return true
}
