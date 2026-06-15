package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// putPassSlotRequest binds (or unbinds) a guest to a cam slot from the greenroom People
// controls (D-20). Slot is a cam label ("cam-1".."cam-8") to bind, or "" to unassign.
type putPassSlotRequest struct {
	Slot string `json:"slot"`
}

// putPassSlotResponse echoes the resulting binding. Live reports whether the change also
// re-routed the LIVE room (true) or was persisted DB-only because the stream isn't live (false):
// the greenroom picker keeps a local override ONLY for a DB-only bind — a live bind is reflected
// by the authoritative roster, so holding an override there could leak past a later unbind (codex).
type putPassSlotResponse struct {
	BoundSlot string `json:"boundSlot"`
	Live      bool   `json:"live"`
}

// putPassSlot is the live slot↔guest (re)bind (AC-6, the DoD core): it persists the binding
// to passes.slot_id AND, if a stream is live, re-routes /s/{slot} by bumping the slot epoch
// (Room.Rebind) — so the host swaps a slot's occupant with NO OBS edit (EN-3). Host-only
// (RequireHost); same-host + at-most-one-active-occupant-per-slot enforced (RF-2). A
// move from another slot also vacates the old slot live (rebind alone never frees it).
func (a *apiServer) putPassSlot(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Serialize this host's binding ops (with the /ws join-replay) so the DB write and the live
	// room command are issued atomically — the room then sees them in DB-commit order (D-20).
	defer a.binds.lock(host.ID)()
	pass, _, ok := a.ownedPass(w, r)
	if !ok {
		return
	}
	var req putPassSlotRequest
	if !readJSON(w, r, &req) {
		return
	}

	// A live reroute of the host-global room is in-scope ONLY when this pass's stream is the
	// host's active session (EN-2/D-20). The DB binding is always persisted (it's the pass's own
	// stream), but touching the LIVE room for an upcoming/non-live stream's guest would disturb
	// the on-air slot pool — symmetric with the join-replay gate (codex).
	live := a.streamIsLive(r.Context(), host.ID, pass.StreamID)

	// Unassign: clear the persistent binding + vacate whatever cam slot the guest holds LIVE
	// (keyed on live occupancy, so a concurrent move can't leave a stale slot bound).
	if strings.TrimSpace(req.Slot) == "" {
		if err := a.store.ClearPassSlot(r.Context(), pass.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "could not unassign slot")
			return
		}
		if live {
			a.liveUnbind(host.ID, pass.ID)
		}
		writeJSON(w, http.StatusOK, putPassSlotResponse{BoundSlot: "", Live: live})
		return
	}

	// A retired (revoked/expired or past-deadline) pass can't go on air: binding it would displace
	// the slot's current occupant and route the OBS source to a guest who can't connect (codex).
	// Reject before AssignPassSlot. (The unassign path above is left open so the host can still
	// clear a retired guest's stale binding.)
	if !passJoinable(pass) {
		writeError(w, http.StatusConflict, "this guest's invite is no longer active")
		return
	}

	// Bind: resolve the cam slot by label.
	idx, ok := parseCamLabel(req.Slot)
	if !ok {
		writeError(w, http.StatusBadRequest, "slot must be a cam slot (cam-1…cam-8)")
		return
	}
	slot, err := a.store.GetHostCamSlot(r.Context(), host.ID, idx)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "slot not provisioned — open the Sources tab first")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load slot")
		return
	}
	// AssignPassSlot atomically displaces any current occupant + assigns (the DoD "swap a slot
	// occupant"); the live Room.Rebind below displaces the prior occupant on air too (EN-3).
	if err := a.store.AssignPassSlot(r.Context(), pass.ID, slot.ID); err != nil {
		switch {
		case errors.Is(err, store.ErrSlotHostMismatch), errors.Is(err, store.ErrSlotNotCam):
			writeError(w, http.StatusBadRequest, "invalid slot for this guest")
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "pass or slot not found")
		default:
			writeError(w, http.StatusInternalServerError, "could not assign slot")
		}
		return
	}

	newLabel := slotLabel(slot)
	if live {
		a.liveRebind(host.ID, newLabel, pass.ID)
	}
	writeJSON(w, http.StatusOK, putPassSlotResponse{BoundSlot: newLabel, Live: live})
}

// streamIsLive reports whether streamID is the host's currently-live session (EN-2/D-20). The
// greenroom picker persists a binding for any of the host's streams, but a LIVE reroute of the
// host-global room must only fire for the live stream — otherwise (re)binding an upcoming
// stream's guest would disturb the on-air slot pool. Fail-closed: no active session, a stream
// mismatch, or a read error all answer false (DB-only), matching the join-replay gate.
func (a *apiServer) streamIsLive(ctx context.Context, hostID, streamID string) bool {
	sess, err := a.store.ActiveSession(ctx, hostID)
	return err == nil && sess.StreamID == streamID
}

// listSlotBindings returns the host's persisted pass→cam-slot bindings (host-only, RequireHost), so
// the greenroom can SEED its picker overrides on load — a pre-live binding is DB-only and isn't in
// the live-occupancy roster, so without this it would vanish from the picker on a refresh / new tab
// (codex). Spans all the host's streams (slots are host-global); the greenroom only renders the
// connected ones.
func (a *apiServer) listSlotBindings(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	m, err := a.store.HostBoundCamPasses(r.Context(), host.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load bindings")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// liveRebind re-routes the live room (if any) to put occupant in newLabel. It uses
// RebindOrVacate (not Rebind): a connected occupant is bound (and the reducer vacates any other
// cam slot it held — one cam slot per occupant), while an OFFLINE occupant leaves the slot
// VACATED (placeholder), never the displaced prior occupant — so live agrees with the DB even
// before the bound guest connects (the /ws join replays it for real). No-op when no stream is
// live (DB-only until it starts).
func (a *apiServer) liveRebind(hostID, newLabel, occupant string) {
	if a.hub == nil {
		return
	}
	if room := a.hub.RoomIfLive(hostID); room != nil {
		room.RebindOrVacate(signaling.SlotID(newLabel), signaling.PeerID(occupant))
	}
}

// liveUnbind vacates whatever cam slot the guest occupies in the live room (if any), keyed on
// the room's live occupancy so a stale label can't strand a slot bound.
func (a *apiServer) liveUnbind(hostID, occupant string) {
	if a.hub == nil {
		return
	}
	if room := a.hub.RoomIfLive(hostID); room != nil {
		room.VacateOccupant(signaling.PeerID(occupant))
	}
}

// ownedPass loads the {id} pass and its stream, confirming the stream belongs to the
// signed-in host (RF-2). A missing pass or a foreign one both answer 404 so ids can't be
// probed.
func (a *apiServer) ownedPass(w http.ResponseWriter, r *http.Request) (*store.Pass, *store.Stream, bool) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}
	pass, err := a.store.GetPass(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "pass not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load pass")
		return nil, nil, false
	}
	stream, err := a.store.GetStream(r.Context(), pass.StreamID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && stream.HostID != host.ID) {
		writeError(w, http.StatusNotFound, "pass not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load stream")
		return nil, nil, false
	}
	return pass, stream, true
}

// parseCamLabel parses a "cam-N" slot label into its index (1..8). It rejects non-cam labels
// (screen/host) and out-of-range indices: pass occupants bind only to cam slots (D-20).
func parseCamLabel(label string) (int64, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(label), "cam-")
	if !ok {
		return 0, false
	}
	idx, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || idx < 1 || idx > camSlotCount {
		return 0, false
	}
	return idx, true
}
