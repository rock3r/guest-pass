package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// wsIdentity is the resolved identity of a /ws connection, derived from its credential
// against LIVE DB state (EN-6) — never from a frame body (EN-7). The hub routes by
// session; the room keys peers by peer; role governs what the connection may do.
type wsIdentity struct {
	// session is the room key. There is one live session per host (D-20/AA-1), so every
	// credential — host cookie, guest pass, OBS slot source — resolves to the same room
	// via the host id. A host-global slot has no stream, so the host id is the only key
	// that unifies a slot source with the stream's guests.
	session string
	// peer is the stable per-identity id used for signal addressing and slot occupancy.
	// Host = "host" (one per room); guest/cohost = pass id (stable across reconnect, D-40);
	// OBS source = "src-"+slot label.
	peer signaling.PeerID
	// role is host | guest | cohost | obs | obs_screen, derived from the credential.
	role string
	// name is the display name folded into the roster (EN-8), resolved from auth (the host's
	// account name or the guest pass name) — never from a frame (EN-7). "" for OBS sources.
	name string
	// slot is the slot an OBS source subscribes to ("cam-1"/"host"/"screen"); "" otherwise.
	slot signaling.SlotID
}

// isSource reports whether this identity is an OBS browser-source page. Only sources may
// send a literal "null" Origin (OBS-CEF) and only sources reflect on-air program state.
func (id wsIdentity) isSource() bool {
	return id.role == "obs" || id.role == "obs_screen"
}

// wsAuthError is a handshake rejection carrying the HTTP status to return before the
// upgrade. reason is for logging only and never contains the token (EN-16).
type wsAuthError struct {
	status int
	reason string
}

func (e *wsAuthError) Error() string { return e.reason }

// wsStore is the narrow slice of the store the /ws handshake needs to resolve a
// credential. *store.Store satisfies it.
type wsStore interface {
	GetHost(ctx context.Context, id string) (*store.Host, error)
	GetStream(ctx context.Context, id string) (*store.Stream, error)
	GetPass(ctx context.Context, id string) (*store.Pass, error)
	GetPassByTokenHash(ctx context.Context, tokenHash string) (*store.Pass, error)
	GetSlot(ctx context.Context, id string) (*store.Slot, error)
	GetSlotBySourceTokenHash(ctx context.Context, tokenHash string) (*store.Slot, error)
	// ActiveSession gates the join-replay on which of the host's streams is currently live (EN-2).
	ActiveSession(ctx context.Context, hostID string) (*store.Session, error)
	RecordSlotTokenUse(ctx context.Context, slotID, sourceIP string) error
	// SetPassStatus backs a kick's token invalidation (D-25): revoking the target's pass so a
	// reconnect is refused at the handshake (passJoinable → false).
	SetPassStatus(ctx context.Context, id, status string) error
}

// tokenHasher is the subset of *token.Hasher the resolver uses.
type tokenHasher interface {
	Hash(raw string) string
}

// wsResolver turns a /ws request's credential into a wsIdentity, rejecting unknown,
// revoked, expired, or suspended principals (AC-1). It shares the live host-session
// authz with the HTTP middleware via auth.Authenticator (EN-6).
type wsResolver struct {
	auth   *auth.Authenticator
	hasher tokenHasher
	store  wsStore
}

// resolve authenticates the request by credential and returns the room/peer/role/slot it
// maps to. Exactly one credential is honored, in priority order: ?pass, then ?src, then
// the session cookie. DB lookups happen BEFORE the WebSocket upgrade, so a rejection is a
// clean HTTP status (no half-open socket).
func (wr *wsResolver) resolve(r *http.Request) (wsIdentity, *wsAuthError) {
	ctx := r.Context()
	q := r.URL.Query()
	switch {
	case q.Has("pass"):
		return wr.resolvePass(ctx, q.Get("pass"))
	case q.Has("src"):
		return wr.resolveSource(ctx, q.Get("src"), r)
	default:
		return wr.resolveCookie(ctx, r)
	}
}

func (wr *wsResolver) resolveCookie(ctx context.Context, r *http.Request) (wsIdentity, *wsAuthError) {
	cookie, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		return wsIdentity{}, &wsAuthError{http.StatusUnauthorized, "no credential"}
	}
	host, err := wr.auth.AuthenticateSessionToken(ctx, cookie.Value)
	switch {
	case errors.Is(err, auth.ErrUnauthorized):
		return wsIdentity{}, &wsAuthError{http.StatusUnauthorized, "invalid session"}
	case errors.Is(err, auth.ErrForbidden):
		return wsIdentity{}, &wsAuthError{http.StatusForbidden, "host not active"}
	case err != nil:
		return wsIdentity{}, &wsAuthError{http.StatusInternalServerError, "host lookup failed"}
	}
	return wsIdentity{session: host.ID, peer: "host", role: "host", name: host.Name}, nil
}

func (wr *wsResolver) resolvePass(ctx context.Context, raw string) (wsIdentity, *wsAuthError) {
	pass, err := wr.store.GetPassByTokenHash(ctx, wr.hasher.Hash(raw))
	if errors.Is(err, store.ErrNotFound) {
		return wsIdentity{}, &wsAuthError{http.StatusUnauthorized, "unknown pass"}
	}
	if err != nil {
		return wsIdentity{}, &wsAuthError{http.StatusInternalServerError, "pass lookup failed"}
	}
	if !passJoinable(pass) {
		return wsIdentity{}, &wsAuthError{http.StatusForbidden, "pass not joinable: " + pass.Status}
	}
	// The pass's host must be active right now (EN-6): a suspended host's guests can't join.
	stream, err := wr.store.GetStream(ctx, pass.StreamID)
	if err != nil {
		return wsIdentity{}, &wsAuthError{http.StatusInternalServerError, "stream lookup failed"}
	}
	if aerr := wr.requireActiveHost(ctx, stream.HostID); aerr != nil {
		return wsIdentity{}, aerr
	}
	// One live session per host (EN-2/D-20): if the host is live for a DIFFERENT stream, this
	// guest's show isn't the on-air one — REFUSE admission so a non-live-stream guest can't enter
	// the host-scoped room and see/mesh with the live session's peers (the replay gate alone left
	// the socket admitted, codex). No active session = pre-live: admit (the host isn't live yet);
	// Go live evicts any straggler from another stream. Fail-closed on a lookup error.
	switch sess, serr := wr.store.ActiveSession(ctx, stream.HostID); {
	case errors.Is(serr, store.ErrNotFound):
		// no live session yet — pre-live, admit
	case serr != nil:
		return wsIdentity{}, &wsAuthError{http.StatusInternalServerError, "session lookup failed"}
	case sess.StreamID != pass.StreamID:
		return wsIdentity{}, &wsAuthError{http.StatusForbidden, "stream not live"}
	}
	name := ""
	if pass.Name != nil {
		name = *pass.Name
	}
	return wsIdentity{session: stream.HostID, peer: signaling.PeerID(pass.ID), role: pass.Role, name: name}, nil
}

// guestAdmissible re-checks at JOIN time (under the per-host binding lock) that a guest/co-host may
// enter the host's room: admit when there's no live session yet (pre-live) or the live session is
// for the guest's OWN stream; refuse otherwise. The handshake already gated admission, but a
// concurrent goLive can make a DIFFERENT stream live in the window between the handshake and Join —
// re-checking under the lock (which goLive also holds) closes that TOCTOU so a non-live-stream
// guest can't slip into the now-live room after the straggler eviction ran (codex). Fail-closed on
// a lookup error.
func (wr *wsResolver) guestAdmissible(ctx context.Context, passID, hostID string) bool {
	pass, err := wr.store.GetPass(ctx, passID)
	if err != nil || !passJoinable(pass) {
		// Re-check joinability too: a pass revoked or past its expires_at deadline SINCE the
		// handshake must not be admitted just because the active session matches (codex).
		return false
	}
	switch sess, serr := wr.store.ActiveSession(ctx, hostID); {
	case errors.Is(serr, store.ErrNotFound):
		return true // no live session yet — pre-live, admit
	case serr != nil:
		return false // fail-closed
	default:
		return sess.StreamID == pass.StreamID
	}
}

// guestBoundSlot re-reads a guest's persisted cam-slot binding (passes.slot_id resolved to its
// label) at REPLAY time — AFTER Join, not at the handshake — so a host PUT during the join
// window can't make the replay route from a stale binding. It returns a label ONLY when the
// binding's stream is the host's currently-LIVE session (EN-2/D-20): the slot pool is host-global
// and the room host-scoped, so without this gate a guest of a non-live stream whose pass carries
// a (legitimately) preassigned slot could auto-bind into the on-air pool just by opening their
// link. Returns "" for an unbound guest, a non-cam binding, a guest of a non-live stream, no live
// session, or any lookup miss (best-effort: a miss just means no replay).
func (wr *wsResolver) guestBoundSlot(ctx context.Context, passID, hostID string) signaling.SlotID {
	pass, err := wr.store.GetPass(ctx, passID)
	if err != nil || pass.SlotID == nil {
		return ""
	}
	sess, err := wr.store.ActiveSession(ctx, hostID)
	if err != nil || sess.StreamID != pass.StreamID {
		return "" // host not live, or this guest belongs to a stream that isn't the live one
	}
	slot, err := wr.store.GetSlot(ctx, *pass.SlotID)
	if err != nil || slot.Kind != store.SlotCam {
		return ""
	}
	label, _ := slotLabelRole(slot)
	return label
}

// passCeiling resolves the program quality ceiling (D-19/AC-8) for a publishing participant from
// its pass's stream, so the guest/co-host caps its program encoder the moment it publishes — pre-
// live or live (a guest may publish before Go live). Returns ok=false on any lookup miss or a stream
// with no ceiling set (CreateStream defaults it, so that is the unusual case). int-narrowed from the
// nullable columns; the values are small.
func (wr *wsResolver) passCeiling(ctx context.Context, passID string) (maxRes, maxFps, maxBitrateKbps int, ok bool) {
	pass, err := wr.store.GetPass(ctx, passID)
	if err != nil {
		return 0, 0, 0, false
	}
	stream, err := wr.store.GetStream(ctx, pass.StreamID)
	if err != nil || stream.MaxRes == nil || stream.MaxFPS == nil || stream.MaxBitrateKbps == nil {
		return 0, 0, 0, false
	}
	return int(*stream.MaxRes), int(*stream.MaxFPS), int(*stream.MaxBitrateKbps), true
}

func (wr *wsResolver) resolveSource(ctx context.Context, raw string, r *http.Request) (wsIdentity, *wsAuthError) {
	slot, err := wr.store.GetSlotBySourceTokenHash(ctx, wr.hasher.Hash(raw))
	if errors.Is(err, store.ErrNotFound) {
		return wsIdentity{}, &wsAuthError{http.StatusUnauthorized, "unknown source token"}
	}
	if err != nil {
		return wsIdentity{}, &wsAuthError{http.StatusInternalServerError, "slot lookup failed"}
	}
	if aerr := wr.requireActiveHost(ctx, slot.HostID); aerr != nil {
		return wsIdentity{}, aerr
	}
	// Stamp leak-detection metadata so a host can spot an unexpected live subscription
	// (AD-23/EN-5). Best-effort: a write failure must not block a legitimate source.
	_ = wr.store.RecordSlotTokenUse(ctx, slot.ID, ClientIP(r))
	label, role := slotLabelRole(slot)
	return wsIdentity{session: slot.HostID, peer: "src-" + signaling.PeerID(label), role: role, slot: label}, nil
}

// sourceStillValid re-checks that a slot source token STILL resolves a slot — used to close
// the resolve→Join window where a D-22 rotation could land after the handshake authenticated
// but before the room admitted the source (a TOCTOU; the rotated hash no longer resolves). The
// full close (a per-session media grant gating admission by token generation) is v1.1
// (AD-23/RF-3). A token-family value is never logged (EN-16).
func (wr *wsResolver) sourceStillValid(ctx context.Context, rawSrc string) bool {
	_, err := wr.store.GetSlotBySourceTokenHash(ctx, wr.hasher.Hash(rawSrc))
	return err == nil
}

// requireActiveHost loads a host live and rejects unless it is active (EN-6).
func (wr *wsResolver) requireActiveHost(ctx context.Context, hostID string) *wsAuthError {
	host, err := wr.store.GetHost(ctx, hostID)
	if errors.Is(err, store.ErrNotFound) {
		return &wsAuthError{http.StatusForbidden, "host missing"}
	}
	if err != nil {
		return &wsAuthError{http.StatusInternalServerError, "host lookup failed"}
	}
	if host.Status != store.HostActive {
		return &wsAuthError{http.StatusForbidden, "host not active"}
	}
	return nil
}

// passJoinable reports whether a pass may currently authenticate a WS connection: not
// revoked, not expired (by status or deadline). created/sent/opened/accepted all join.
func passJoinable(p *store.Pass) bool {
	switch p.Status {
	case store.PassRevoked, store.PassExpired:
		return false
	}
	if p.ExpiresAt != nil && time.Now().Unix() >= *p.ExpiresAt {
		return false
	}
	return true
}

// slotLabelRole maps a DB slot to its signaling slot label and the OBS source role. Slot
// labels are the canonical kind-qualified grammar (cam-N | host | screen, RF-26).
func slotLabelRole(s *store.Slot) (signaling.SlotID, string) {
	switch s.Kind {
	case store.SlotScreenshare:
		return "screen", "obs_screen"
	case store.SlotHost:
		return "host", "obs"
	default: // cam
		idx := int64(0)
		if s.Idx != nil {
			idx = *s.Idx
		}
		return signaling.SlotID("cam-" + strconv.FormatInt(idx, 10)), "obs"
	}
}

// redactWSURL renders a /ws request path with the secret token query params (pass, src)
// masked, for safe logging (EN-16). The remaining params are preserved.
func redactWSURL(u *url.URL) string {
	q := u.Query()
	for _, k := range []string{"pass", "src"} {
		if q.Has(k) {
			q.Set(k, "REDACTED")
		}
	}
	out := u.Path
	if enc := q.Encode(); enc != "" {
		out += "?" + enc
	}
	return out
}
