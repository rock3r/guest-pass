package signaling

import "sort"

// Roster projection (EN-8). The roster is a per-recipient server projection filtered by
// the recipient's rank. For the M2 tracer the entry shape is minimal ({id, role}); the
// full shape (cam/mic/level/locks/on-air) lands with the M3 greenroom.
//
// The one rank distinction M2 makes: the OBS source virtual peers (obs/obs_screen, one
// per slot source page) are **host-only** — guests and co-hosts never see them. OBS
// source pages are minimal (EN-13) and receive no roster of their own; they only learn
// their slot binding via slot-rebind.

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

// rosterFor builds the roster projection a peer of recipientRole should see. Entries are
// sorted by id so the projection is deterministic (map iteration is randomized).
func (s *roomState) rosterFor(recipientRole string) []RosterEntry {
	entries := make([]RosterEntry, 0, len(s.peers))
	for id, p := range s.peers {
		if visibleTo(p.role, recipientRole) {
			entries = append(entries, RosterEntry{ID: string(id), Role: p.role})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries
}
