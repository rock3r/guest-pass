package signaling

// canKick reports whether actor may kick target (D-25 moderation): both must be participants,
// and the actor must be STRICTLY above the target's current rank — the host kicks co-hosts and
// guests, a co-host kicks guests, a guest kicks no one, and nobody kicks the host (immune) or a
// peer of equal rank. Evaluated against current rank (demotion-safe, EN-7).
func (s *roomState) canKick(actor, target PeerID) bool {
	a, t := s.peers[actor], s.peers[target]
	if a == nil || t == nil || !isParticipant(a.role) || !isParticipant(t.role) {
		return false
	}
	return rankOf(a.role) > rankOf(t.role)
}

// kickPeer tears a target out of the room (D-25 cooperative teardown). It reuses leave(), which
// — ATOMICALLY, before any teardown broadcast (EN-3) — clears any slot the target occupied and
// bumps that slot's epoch (so a reconnecting modified source resolves to placeholder, not the
// kicked occupant), tells the source, removes the target from room state, and tells the others
// it left (peer-left + roster). It then sends the target a TERMINAL {t:terminate,kicked} so its
// client routes to the "removed by host" screen (EN-9). The Room evicts the target's connection
// after delivering these (the buffered terminate flushes before the socket closes); token
// invalidation + refuse-rejoin are handled by the Room/handler around this call.
func (s *roomState) kickPeer(target PeerID) []outbound {
	outs := s.leave(target, true) // terminal: vacate the slot now (no grace) so a modified source can't re-show the kicked occupant
	return append(outs, outbound{to: target, frame: Frame{T: "terminate", Reason: TerminateKicked}})
}
