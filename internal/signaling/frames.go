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
	Epoch          *int            `json:"epoch,omitempty"` // pointer so epoch rides ONLY slot frames; &0 still serializes (EN-3)
	Event          string          `json:"event,omitempty"`
	Active         bool            `json:"active,omitempty"`
	OnAir          string          `json:"onAir,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	// Self-presence on an inbound {t:"state"} frame (EN-7): the sender's own cam/mic/screen
	// (a full snapshot, throttled) and the local audio meter. Level rides the room in-memory
	// only and is coalesced onto a separate batched tick (AD-13), never echoed in the roster.
	Cam        bool          `json:"cam,omitempty"`
	Mic        bool          `json:"mic,omitempty"`
	Screen     bool          `json:"screen,omitempty"`
	Level      float64       `json:"level,omitempty"`
	Peers      []RosterEntry `json:"peers,omitempty"`  // roster projection (EN-8)
	Peer       *RosterEntry  `json:"peer,omitempty"`   // the newcomer in a peer-joined frame
	PeerID     string        `json:"peerId,omitempty"` // the departed peer in a peer-left frame
	Recipient  string        `json:"self,omitempty"`   // on a {t:roster}: the recipient's own peer id, so a client can find its self entry (e.g. the guest self on-air pill)
	ICEServers []ICEServer   `json:"iceServers,omitempty"`
	TTLSec     int           `json:"ttlSec,omitempty"` // TURN credential lifetime on a {t:ice} frame (EN-4)
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
	Self       bool          `json:"self,omitempty"` // true only on the recipient's own entry in its projection
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
