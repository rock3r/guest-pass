// Package signaling implements the GuestPass realtime core: an actor/hub-per-room
// model (AD-2) where one goroutine owns each room's state and all mutations flow
// through a command channel — no locks on room state. The hard logic lives in a
// PURE reducer (roomState) so it is exhaustively table-testable with no network or
// browser (AD-5/RF-25). See docs/ARCHITECTURE.md §7 for the protocol.
package signaling

import "encoding/json"

// Frame is the flat JSON envelope for all signaling messages. The server relays
// SDP/ICE verbatim as opaque payloads and never inspects them (D-23).
type Frame struct {
	T              string          `json:"t"`
	To             string          `json:"to,omitempty"`
	From           string          `json:"from,omitempty"`
	SDP            json.RawMessage `json:"sdp,omitempty"`
	ICE            json.RawMessage `json:"ice,omitempty"`
	Slot           string          `json:"slot,omitempty"`
	OccupantPeerID string          `json:"occupantPeerId,omitempty"`
	// Name carries the bound occupant's display name to an OBS source on a {t:slot-rebind} (the
	// nameplate, D-16): the source page gets no roster (EN-13), so the name rides the binding frame.
	// It renders as escaped textContent only, gated by the source's show/hide URL param (EN-15); a
	// later name override re-sends slot-rebind with the SAME occupant+epoch so the source refreshes
	// the nameplate without re-linking media.
	Name  string `json:"name,omitempty"`
	Epoch *int   `json:"epoch,omitempty"` // pointer so epoch rides ONLY slot frames; &0 still serializes (EN-3)
	// LockKinds carries a {t:"occupant-locks"} projection to an OBS source page: the bound occupant's
	// active suppression-lock KINDS (mic|cam|share) so the source detaches the locked REMOTE track from
	// the program output, independent of the (possibly modified) occupant (RF-8 receiver-side). It is
	// deliberately KINDS-ONLY — never applierPeerId/applierRank — since source pages get no roster
	// (EN-13) and the slot token is a permanent crown-jewel credential (EN-5): the source needs which
	// modalities to drop, not who applied them. It rides the slot epoch + occupantPeerId so the source
	// ignores a straggler for a prior occupant/epoch (EN-3). Moderators read the full LockView (with
	// applier) from the roster's locks field instead.
	LockKinds []string `json:"lockKinds,omitempty"`
	Event     string   `json:"event,omitempty"`
	Active    bool     `json:"active,omitempty"`
	OnAir     string   `json:"onAir,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Kind      string   `json:"kind,omitempty"` // {t:release} modality: mic | cam | share (D-13)
	Role      string   `json:"role,omitempty"` // {t:role} target role: cohost | guest (host-only, D-15)
	// Program quality ceiling (D-19/AC-8) on a {t:"ceiling"} frame, server → a publishing participant
	// (guest/co-host): the stream-wide MAX the publisher caps its program/monitor encoder at and the M3
	// degradation ladder recovers no higher (shed below is fine). Delivered on join + re-broadcast when
	// the host adjusts it live. The server never touches media (D-23) — it only relays the numbers.
	MaxRes         int `json:"maxRes,omitempty"`         // max program height in px (e.g. 720)
	MaxFps         int `json:"maxFps,omitempty"`         // max program framerate (e.g. 30)
	MaxBitrateKbps int `json:"maxBitrateKbps,omitempty"` // max program bitrate in kbps (e.g. 2500)
	// Res carries a per-source program-resolution override (D-19/AC-8) on a {t:"source-quality"} frame:
	// an OBS cam source's ?res URL param, relayed by the server to that slot's bound occupant so the
	// occupant caps the sender feeding THAT source to res px — a per-guest cap on top of the ceiling.
	Res    int    `json:"res,omitempty"`
	Text   string `json:"text,omitempty"`   // {t:chat} backstage message — relayed only, NEVER persisted or logged (EN-20)
	Raised bool   `json:"raised,omitempty"` // {t:hand} hand-raise state — true = raise (self), false = lower (self) / dismiss (host)
	// Self-presence on an inbound {t:"state"} frame (EN-7): the sender's own cam/mic/screen
	// and the local audio meter. These are POINTERS so an ABSENT field means "leave it
	// unchanged" — a meter-only update ({"t":"state","level":…}) must not clobber presence to
	// false, and a presence-only update must not zero the meter (a plain value would unmarshal
	// absent → zero). Level is held in-memory only and coalesced onto the {t:levels} tick
	// (AD-13), never echoed in the roster (EN-11: no per-frame persistence).
	Cam    *bool              `json:"cam,omitempty"`
	Mic    *bool              `json:"mic,omitempty"`
	Screen *bool              `json:"screen,omitempty"`
	Level  *float64           `json:"level,omitempty"`
	Levels map[string]float64 `json:"levels,omitempty"` // {t:levels} batched meter tick: peerId → level (AD-13)
	// Self-degradation report on an inbound {t:"stats"} frame (AD-21): the publisher's own coarse
	// connection-health signal (1..5), RTT estimate, and active shedding state (Degraded nil = not
	// degraded). Folded into the sender's roster entry; held in memory only (EN-11). Signal/RttMs
	// reuse the RosterEntry json keys so a stats frame and a roster entry read the same.
	Signal   int           `json:"signal,omitempty"`
	RttMs    int           `json:"rttMs,omitempty"`
	Degraded *DegradedView `json:"degraded,omitempty"`
	Peers    []RosterEntry `json:"peers,omitempty"`  // roster projection (EN-8)
	Peer     *RosterEntry  `json:"peer,omitempty"`   // the newcomer in a peer-joined frame
	PeerID   string        `json:"peerId,omitempty"` // a string peer id: the departed peer (peer-left, out) or the moderation target of an inbound force/release (D-13) — distinct from the `peer` OBJECT in peer-joined
	// Screenshare preview-switcher (D-21/AC-11). Previews + Live ride the HOST-ONLY {t:"screen-roster"}
	// broadcast: Previews is the pool of peer ids actively sharing (the backstage rail), Live is the
	// one the host selected on-air ("" = none, no auto-advance). {t:"screen-select"} carries the
	// target in PeerID ("" clears the slot). A sharer learns its OWN active-live bit from the
	// screenShare pointer folded into its roster entry (AC-13), NOT from this host-only frame.
	//
	// {t:"screen-roster"} is a FULL-STATE SNAPSHOT, not a delta: the host client REPLACES its rail +
	// live selection on every frame, so an OMITTED previews means "empty pool" and an OMITTED live
	// means "no live share" (omitempty drops []/""). A clearing — last sharer stops or the slot is
	// cleared — therefore arrives as {t:"screen-roster"} with these fields absent, and the host resets.
	Previews   []string    `json:"previews,omitempty"`
	Live       string      `json:"live,omitempty"`
	Recipient  string      `json:"self,omitempty"` // on a {t:roster}: the recipient's own peer id, so a client can find its self entry (e.g. the guest self on-air pill)
	ICEServers []ICEServer `json:"iceServers,omitempty"`
	TTLSec     int         `json:"ttlSec,omitempty"` // TURN credential lifetime on a {t:ice} frame (EN-4)
}

// ICEServer is one entry of the WebRTC ICE configuration the server hands a peer in the
// {t:"ice"} join-ack (AD-14). URLs holds stun:/turn(s): URLs; Username/Credential are set
// only for a TURN entry. STUN is always offered (D-38); the TURN entry and its ephemeral
// HMAC credential (EN-4) are added when a relay is configured (M2). The shape matches the
// browser RTCIceServer dictionary so the client can pass it straight to RTCPeerConnection.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// RosterEntry is a peer's per-recipient projection in a roster / peer-joined frame (EN-8).
// It carries the full greenroom entry shape (AC-1); not every field is driven yet — the
// M3 PRs each populate their slice (locks: PR-3, handRaised: PR-7, signal/rttMs/degraded:
// PR-13). cam/mic/screen are always serialized for participants (a muted guest is cam:false,
// not "unknown"); the on-air pill is the three-state OBS reflection folded in from the slot
// (D-24). The live audio meter is NOT here — it rides the batched {t:levels} tick (AD-13).
type RosterEntry struct {
	ID         string        `json:"id"`
	Name       string        `json:"name,omitempty"`
	Role       string        `json:"role"`
	Cam        bool          `json:"cam"`
	Mic        bool          `json:"mic"`
	Screen     bool          `json:"screen"`
	HandRaised bool          `json:"handRaised,omitempty"`
	OnAir      string        `json:"onAir,omitempty"`
	Signal     int           `json:"signal,omitempty"`
	RttMs      int           `json:"rttMs,omitempty"`
	Degraded   *DegradedView `json:"degraded,omitempty"`
	Locks      []LockView    `json:"locks,omitempty"`
	BoundSlot  string        `json:"boundSlot,omitempty"` // HOST-ONLY: the cam slot this participant occupies live (e.g. "cam-1"), for the greenroom People controls (D-15/D-20); stripped from non-host projections
	CanScreen  bool          `json:"canScreen,omitempty"` // screenshare eligibility (EN-23/AC-9): host-managed policy — visible to the HOST (every entry, for the grant/revoke toggle) and to a guest on its OWN entry (its share affordance); stripped from a non-host's view of OTHERS
	// ScreenShare is the participant's screenshare preview-switcher state (D-21/AC-11/AC-13):
	// "backstage" = actively sharing into the host preview rail; "live" = the host selected it on-air;
	// "" = not sharing. "live" is visible to EVERYONE (so all clients render the live share); "backstage"
	// is host-only — a non-host sees only its OWN backstage state (its self active-backstage indicator),
	// never another participant's (the preview rail is host-only, D-21).
	ScreenShare string `json:"screenShare,omitempty"`
	Self        bool   `json:"self,omitempty"` // true only on the recipient's own entry in its projection
}

// LockView is a suppression lock as seen in a roster entry (D-13/EN-7). applierRank tells
// clients WHO may release it: the applier, anyone at or above the rank floor, or the host.
// Populated by PR-3.
type LockView struct {
	Kind          string `json:"kind"` // mic | cam | share
	ApplierPeerID string `json:"applierPeerId,omitempty"`
	ApplierRank   string `json:"applierRank"` // host | cohost
}

// DegradedView reflects an active per-publisher degradation (AD-21) in a roster entry.
// Populated by PR-13.
type DegradedView struct {
	Dir    string `json:"dir"`    // lowering | recovering
	Reason string `json:"reason"` // cpu | bandwidth
}

// Three-state on-air values (D-24).
const (
	OnAirYes     = "on-air"
	OnAirNo      = "not-on-air"
	OnAirUnknown = "status-unavailable"
)

// Terminate-reason taxonomy (EN-9). The server sends {t:terminate,reason} BEFORE closing a
// socket so the client routes correctly: a TRANSIENT reason reconnects with backoff (keyed by
// pass_id); a TERMINAL reason stops and routes to the matching error screen. An ABSENT terminate
// frame is treated as transient unless reconnect-time validation is terminal (RF-22).
const (
	TerminateReconnect    = "reconnect"     // TRANSIENT — server drain / eviction / network blip
	TerminateKicked       = "kicked"        // TERMINAL — removed by host (D-25)
	TerminateExpired      = "expired"       // TERMINAL — pass past its deadline
	TerminateRevoked      = "revoked"       // TERMINAL — pass revoked
	TerminateSessionEnded = "session-ended" // TERMINAL — host ended the stream (D-40; emitted in M4)
	TerminateTokenRotated = "token-rotated" // TERMINAL — slot token rotated (D-22; Room.RotateSource)
)
