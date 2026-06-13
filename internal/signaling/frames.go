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
	Peers          []RosterEntry   `json:"peers,omitempty"`  // roster projection (EN-8)
	Peer           *RosterEntry    `json:"peer,omitempty"`   // the newcomer in a peer-joined frame
	PeerID         string          `json:"peerId,omitempty"` // the departed peer in a peer-left frame
	ICEServers     []ICEServer     `json:"iceServers,omitempty"`
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

// RosterEntry is a peer's projection in a roster / peer-joined frame. The M2 tracer
// ships the minimal {id, role} shape; the richer fields (name, cam/mic/level, on-air,
// locks) land with the M3 greenroom. The rank filtering itself (EN-8) is implemented in
// M2 — see roster.go.
type RosterEntry struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

// Three-state on-air values (D-24).
const (
	OnAirYes     = "on-air"
	OnAirNo      = "not-on-air"
	OnAirUnknown = "status-unavailable"
)
