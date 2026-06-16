package web

import (
	"errors"
	"net/http"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// putCeilingRequest adjusts a stream's program quality ceiling (D-19/AC-8): the max resolution
// (height px), framerate, and bitrate (kbps) the program encoder is capped at.
type putCeilingRequest struct {
	MaxRes         int `json:"maxRes"`
	MaxFps         int `json:"maxFps"`
	MaxBitrateKbps int `json:"maxBitrateKbps"`
}

// putCeilingResponse echoes the CLAMPED ceiling now in effect (so the host UI reflects the server's
// sane-bounds clamp) and whether the change also re-capped the LIVE room (true) or was persisted
// DB-only because the stream isn't the host's active session (false).
type putCeilingResponse struct {
	MaxRes         int  `json:"maxRes"`
	MaxFps         int  `json:"maxFps"`
	MaxBitrateKbps int  `json:"maxBitrateKbps"`
	Live           bool `json:"live"`
}

// Ceiling clamp bounds (D-19): a sane window so a hostile or fat-fingered value can't drive the
// program encoder to absurd settings (e.g. a 0 that would divide-by-zero on the client, or a
// multi-Gbps bitrate). The defaults (720/30/2500) sit comfortably inside.
const (
	minMaxRes, maxMaxRes                 = 144, 2160
	minMaxFps, maxMaxFps                 = 1, 60
	minMaxBitrateKbps, maxMaxBitrateKbps = 100, 20000
)

// putStreamCeiling adjusts a stream's program quality ceiling (AC-8/D-19). It clamps the values to
// sane bounds (EN-15-style server-side validation), persists streams.max_*, and — when the stream is
// the host's live session — broadcasts {t:ceiling} so live publishers re-cap their program encoder
// + clamp degradation recovery to it. Host-only (RequireHost); RF-2 same-host via ownedStream.
func (a *apiServer) putStreamCeiling(w http.ResponseWriter, r *http.Request) {
	stream, ok := a.ownedStream(w, r)
	if !ok {
		return
	}
	var req putCeilingRequest
	if !readJSON(w, r, &req) {
		return
	}
	mr := clampInt(req.MaxRes, minMaxRes, maxMaxRes)
	mf := clampInt(req.MaxFps, minMaxFps, maxMaxFps)
	mb := clampInt(req.MaxBitrateKbps, minMaxBitrateKbps, maxMaxBitrateKbps)

	r64, f64, b64 := int64(mr), int64(mf), int64(mb)
	stream.MaxRes, stream.MaxFPS, stream.MaxBitrateKbps = &r64, &f64, &b64
	if err := a.store.UpdateStream(r.Context(), stream); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update the quality ceiling")
		return
	}
	// Re-cap the LIVE room only when this stream is the host's active session (symmetric with the
	// slot-bind / nameplate gates): the host-global room belongs to the live stream. The DB write
	// above always persists; a guest joining later reads the new value (passCeiling).
	live := a.streamIsLive(r.Context(), stream.HostID, stream.ID)
	if live {
		a.liveCeiling(stream.HostID, mr, mf, mb)
	}
	writeJSON(w, http.StatusOK, putCeilingResponse{MaxRes: mr, MaxFps: mf, MaxBitrateKbps: mb, Live: live})
}

// sessionCeilingResponse is the host's active session's stream id + current ceiling, for the
// greenroom control. StreamID is the PUT target; the values populate the inputs.
type sessionCeilingResponse struct {
	StreamID       string `json:"streamId"`
	MaxRes         int    `json:"maxRes"`
	MaxFps         int    `json:"maxFps"`
	MaxBitrateKbps int    `json:"maxBitrateKbps"`
}

// getSessionCeiling returns the host's ACTIVE session's stream id + program quality ceiling so the
// greenroom can populate + target its ceiling control (D-19/AC-8). 404 when no session is live (the
// control stays hidden until Go live). Host-only (RequireHost). Read-only.
func (a *apiServer) getSessionCeiling(w http.ResponseWriter, r *http.Request) {
	host, ok := auth.HostFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	sess, err := a.store.ActiveSession(r.Context(), host.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no live session")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load session")
		return
	}
	stream, err := a.store.GetStream(r.Context(), sess.StreamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load stream")
		return
	}
	writeJSON(w, http.StatusOK, sessionCeilingResponse{
		StreamID:       stream.ID,
		MaxRes:         derefInt(stream.MaxRes),
		MaxFps:         derefInt(stream.MaxFPS),
		MaxBitrateKbps: derefInt(stream.MaxBitrateKbps),
	})
}

// derefInt narrows a nullable int64 column to int (0 when nil).
func derefInt(v *int64) int {
	if v == nil {
		return 0
	}
	return int(*v)
}

// liveCeiling broadcasts the ceiling to the live room's publishers (if any). No-op when no stream
// is live (DB-only until it starts; a guest joining then reads the persisted value).
func (a *apiServer) liveCeiling(hostID string, maxRes, maxFps, maxBitrateKbps int) {
	if a.hub == nil {
		return
	}
	if room := a.hub.RoomIfLive(hostID); room != nil {
		room.SetCeiling(maxRes, maxFps, maxBitrateKbps)
	}
}

// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
