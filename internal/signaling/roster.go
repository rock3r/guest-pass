package signaling

import "sort"

// Roster projection (EN-8). The roster is a per-recipient server projection filtered by the
// recipient's rank. The M3 greenroom carries the full entry shape (name, cam/mic/screen,
// handRaised, three-state on-air, signal/degraded, locks) — see entryFor; the per-PR fields
// (locks: PR-3, handRaised: PR-7, signal/degraded: PR-13) populate over the milestone.
//
// Rank distinctions: the OBS source virtual peers (obs/obs_screen, one per slot source page)
// are host-only — guests and co-hosts never see them. OBS source pages are minimal (EN-13)
// and receive no roster of their own; they only learn their slot binding via slot-rebind.

// isParticipant reports whether a role is a greenroom participant (host/co-host/guest),
// as opposed to an OBS source virtual peer. Only participants receive roster frames and
// only participants appear in a non-host projection.
func isParticipant(role string) bool {
	return role == "host" || role == "cohost" || role == "guest"
}

// visibleTo reports whether a peer of peerRole appears in recipientRole's roster
// projection (EN-8): the host sees everyone including the obs source virtual peers; a
// participant sees only other participants.
func visibleTo(peerRole, recipientRole string) bool {
	if recipientRole == "host" {
		return true
	}
	return isParticipant(peerRole)
}

// entryFor builds a peer's base roster entry: identity for everyone, and the live greenroom
// fields (presence + folded three-state on-air) only for participants — an OBS source virtual
// peer is a connection marker, not a person, so it carries no presence/on-air. The per-PR
// fields (locks, signal/degraded) are layered on by their PRs. The recipient-specific Self
// marker is set by rosterFor, not here.
func (s *roomState) entryFor(p *peerInfo) RosterEntry {
	e := RosterEntry{ID: string(p.id), Name: p.name, Role: p.role}
	if isParticipant(p.role) {
		e.Cam, e.Mic, e.Screen = p.cam, p.mic, p.screen
		e.HandRaised = p.handRaised
		e.OnAir = s.onAirFor(p.id)
		e.Locks = s.locksOf(p.id) // live-visible suppression locks (D-13/EN-7), with applierRank
	}
	return e
}

// onAirFor aggregates a participant's three-state on-air across the cam slot(s) it occupies:
// on-air if ANY occupied slot is active, else not-on-air if any is definitely not, else
// status-unavailable (no slot, or no live OBS signal) — the M3 multi-slot-aggregation default
// (screenshare on-air is moot in M3; it's moderation-only). Never asserts on-air without a
// live signal behind it (D-24).
func (s *roomState) onAirFor(id PeerID) string {
	state := OnAirUnknown
	for _, st := range s.slots {
		if st.occupant != id {
			continue
		}
		if st.onAir == OnAirYes {
			return OnAirYes
		}
		if st.onAir == OnAirNo {
			state = OnAirNo
		}
	}
	return state
}

// rosterFor builds the roster projection a recipient should see, marking the recipient's own
// entry with Self so a client can locate itself (e.g. the guest self on-air pill). Entries are
// sorted by id so the projection is deterministic (map iteration is randomized).
func (s *roomState) rosterFor(recipientID PeerID, recipientRole string) []RosterEntry {
	entries := make([]RosterEntry, 0, len(s.peers))
	for id, p := range s.peers {
		if !visibleTo(p.role, recipientRole) {
			continue
		}
		e := s.entryFor(p)
		if id == recipientID {
			e.Self = true
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries
}
