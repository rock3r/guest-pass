package signaling

// Suppression-lock state machine (D-13 / EN-7). Forces are SUPPRESSIVE-ONLY (they push a
// modality toward off; no rank can force a modality on — consent) and AUTHORITY-LOCKED: a
// force at rank R holds until released by a current rank ≥ R or the host; the target can
// never self-release. The server is the sole authority — UI gating is bypassable (EN-7).
//
// The lock kinds (modalities) are mic | cam | share, mapped to the presence fields
// mic | cam | screen respectively. force-mute → mic, force-no-cam → cam, force-no-share →
// share. Each force suppresses the matching presence at source and rejects any self-{t:state}
// that tries to re-enable it.

// Rank ordering (D-15): Host > Co-host > Guest. OBS source virtual peers are not participants
// and have no rank (rankNone); they can neither force nor be forced.
const (
	rankNone   = -1
	rankGuest  = 0
	rankCohost = 1
	rankHost   = 2
)

// lockKinds is the canonical modality order, for a deterministic roster locks[] projection.
var lockKinds = []string{"mic", "cam", "share"}

func rankOf(role string) int {
	switch role {
	case "host":
		return rankHost
	case "cohost":
		return rankCohost
	case "guest":
		return rankGuest
	default: // obs / obs_screen — not a participant
		return rankNone
	}
}

// rankName maps a rank floor back to its applierRank string for the roster (EN-8): the lock
// floor is always cohost or host (a guest can never be an applier).
func rankName(r int) string {
	if r >= rankHost {
		return "host"
	}
	return "cohost"
}

// modalityPresence returns a pointer to the presence flag a lock modality suppresses, so a
// force can stop it at source: mic → mic, cam → cam, share → screen.
func (p *peerInfo) modalityPresence(modality string) *bool {
	switch modality {
	case "mic":
		return &p.mic
	case "cam":
		return &p.cam
	case "share":
		return &p.screen
	}
	return nil
}

// force applies a suppression force from actor onto target's modality (D-13). Authority is
// evaluated against CURRENT rank (demotion-safe): the actor must be STRICTLY above the target
// (so the host is immune and a guest can never force). On a locked modality a higher-rank
// force RAISES the floor + owner; a lower-or-equal force is a no-op. The force also suppresses
// the matching presence at source. Returns the roster re-broadcast when the lock changes.
func (s *roomState) force(actor, target PeerID, modality string) []outbound {
	a, t := s.peers[actor], s.peers[target]
	if a == nil || t == nil || !isParticipant(a.role) || !isParticipant(t.role) {
		return nil
	}
	if s.modalityIndex(modality) < 0 {
		return nil // unknown modality
	}
	r := rankOf(a.role)
	if r <= rankOf(t.role) {
		return nil // must be strictly above the target (host immune; guests can't force)
	}
	if cur := t.locks[modality]; cur != nil && r <= cur.floor {
		return nil // a lower-or-equal-rank force on an already-locked modality is a no-op
	}
	if t.locks == nil {
		t.locks = map[string]*lockState{}
	}
	t.locks[modality] = &lockState{applier: actor, floor: r}
	// Suppress the modality at source: a force pushes it toward off (the target can't re-enable
	// it while locked — applyState rejects that self-state).
	if pres := t.modalityPresence(modality); pres != nil {
		*pres = false
	}
	return s.rebroadcastRoster()
}

// release lifts a suppression lock (D-13). The target can NEVER self-release; an actor may
// release only with a CURRENT rank ≥ the lock's floor — so the host (rank ≥ any floor) always
// can, a peer at or above the floor can, and a DEMOTED applier that dropped below the floor
// can NO LONGER release (demotion-safe; matches the M3 plan default). The target then
// re-enables the modality itself via {t:state}. Returns the roster re-broadcast on release.
func (s *roomState) release(actor, target PeerID, modality string) []outbound {
	a, t := s.peers[actor], s.peers[target]
	if a == nil || t == nil {
		return nil
	}
	lock := t.locks[modality]
	if lock == nil {
		return nil // nothing locked
	}
	if rankOf(a.role) < lock.floor {
		return nil // not authorized at the actor's current rank (target included: rank < floor)
	}
	delete(t.locks, modality)
	return s.rebroadcastRoster()
}

// modalityIndex returns a stable order index for a lock modality, or -1 if unknown.
func (s *roomState) modalityIndex(modality string) int {
	for i, k := range lockKinds {
		if k == modality {
			return i
		}
	}
	return -1
}

// locksOf projects a peer's active locks into the roster shape (EN-8), in the canonical
// modality order so the projection is deterministic. applierRank tells clients WHO may
// release (the applier, anyone at/above the floor, or the host).
func (s *roomState) locksOf(p *peerInfo) []LockView {
	if len(p.locks) == 0 {
		return nil
	}
	out := make([]LockView, 0, len(p.locks))
	for _, kind := range lockKinds { // canonical mic→cam→share order: deterministic projection
		if lock := p.locks[kind]; lock != nil {
			out = append(out, LockView{Kind: kind, ApplierPeerID: string(lock.applier), ApplierRank: rankName(lock.floor)})
		}
	}
	return out
}
