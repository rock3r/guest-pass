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
	Epoch          int             `json:"epoch,omitempty"`
	Event          string          `json:"event,omitempty"`
	Active         bool            `json:"active,omitempty"`
	OnAir          string          `json:"onAir,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Peers          []RosterEntry   `json:"peers,omitempty"`
}

// RosterEntry is a peer's projection in a roster frame (minimal for SPIKE-2; the
// role-filtered projection EN-8 is fleshed out in M3).
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
