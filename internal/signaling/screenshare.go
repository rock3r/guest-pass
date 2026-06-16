package signaling

import "sort"

// Screenshare preview-switcher (D-21/AC-11). Multiple ELIGIBLE participants share a screen at once;
// each is a backstage preview in screenPreviews (the host-only rail). The host promotes ONE to live
// with {t:screen-select} — the live share occupies the "screen" slot, so /s/screen re-routes
// slot-rebind-style (AC-12) and on-air folds in like any source (D-24). Co-hosts may force-no-share
// a sharer (moderation, D-13) but NEVER select-live (host-only, the one sanctioned D-11 exception).
// A revoke/force/leave pulls a sharer from the pool + the slot if it was live — NO auto-advance.
//
// The canonical {t:screen-roster} broadcast is HOST-ONLY; a sharer learns its own active-backstage
// vs active-live state from the screenShare pointer folded into its OWN roster entry (AC-13), the
// same pattern as the three-state on-air fold.

const screenSlot = SlotID("screen")

// screenLiveID is the currently live sharer — the occupant of the "screen" slot ("" = none). It is
// the single source of truth for the live share; there is no separate field.
func (s *roomState) screenLiveID() PeerID {
	return s.slot(screenSlot).occupant
}

// screenShareStateFor returns a peer's screenshare state for its roster entry (AC-13): "live" if it
// is the selected live sharer, "backstage" if it is actively sharing but not selected, else "".
func (s *roomState) screenShareStateFor(id PeerID) string {
	if s.screenLiveID() == id {
		return "live"
	}
	if s.screenPreviews[id] {
		return "backstage"
	}
	return ""
}

// screenRoster builds the HOST-ONLY {t:screen-roster} broadcast (D-21): the preview pool (sorted for
// a deterministic projection) + the live sharer ("" = none). Non-host participants get the live
// sharer via the screenShare="live" roster fold instead (the preview pool stays host-only).
func (s *roomState) screenRoster() []outbound {
	previews := make([]string, 0, len(s.screenPreviews))
	for id := range s.screenPreviews {
		previews = append(previews, string(id))
	}
	sort.Strings(previews)
	frame := Frame{T: "screen-roster", Previews: previews, Live: string(s.screenLiveID())}
	var out []outbound
	for pid, p := range s.peers {
		if p.role == "host" {
			out = append(out, outbound{to: pid, frame: frame})
		}
	}
	return out
}

// screenStart adds an ELIGIBLE participant to the preview pool (D-21). Server-enforced (EN-7, UI
// gating is bypassable): the sender must be a participant, screenshare-eligible (can_screen, AC-9),
// and not share-locked (a force-no-share'd guest can't share). Idempotent — already in the pool is a
// no-op. Re-broadcasts the roster (the sharer's screenShare pointer = "backstage") + the host-only
// screen-roster (the rail gains the sharer).
func (s *roomState) screenStart(id PeerID) []outbound {
	p := s.peers[id]
	if p == nil || !isParticipant(p.role) || !p.canScreen || s.locked(id, "share") {
		return nil
	}
	if s.screenPreviews[id] {
		return nil
	}
	s.screenPreviews[id] = true
	return append(s.rebroadcastRoster(), s.screenRoster()...)
}

// screenStop removes a participant from the preview pool (D-21) — it stopped sharing. If it was the
// LIVE share, the "screen" slot vacates (placeholder, NO auto-advance). A no-op when the peer wasn't
// sharing. Re-broadcasts the roster + the host-only screen-roster.
func (s *roomState) screenStop(id PeerID) []outbound {
	if !s.screenPreviews[id] && s.screenLiveID() != id {
		return nil
	}
	delete(s.screenPreviews, id)
	var out []outbound
	if s.screenLiveID() == id {
		out = append(out, s.vacateSlot(screenSlot)...) // clear the live slot — no auto-advance
	}
	out = append(out, s.rebroadcastRoster()...)
	return append(out, s.screenRoster()...)
}

// screenSelect promotes one backstage sharer to LIVE in the "screen" slot, or clears it (peer="",
// no auto-advance). HOST-ONLY (D-21, the one sanctioned exception to "OBS owns composition", D-11):
// a non-host actor is a no-op (server-enforced, EN-7 — the dispatch gate is convenience). A select
// of a peer NOT in the preview pool is a no-op (can only promote an active sharer). Re-broadcasts
// the roster (slot frames + the screenShare="live" fold) + the host-only screen-roster.
func (s *roomState) screenSelect(actor, peer PeerID) []outbound {
	a := s.peers[actor]
	if a == nil || rankOf(a.role) != rankHost {
		return nil // host-only
	}
	if peer == "" {
		// Clear the slot → placeholder (no auto-advance). No-op if already empty.
		if s.screenLiveID() == "" {
			return nil
		}
		return append(s.unbindSlot(screenSlot), s.screenRoster()...)
	}
	if !s.screenPreviews[peer] {
		return nil // can only select a peer currently in the preview pool
	}
	if s.screenLiveID() == peer {
		return nil // already live — no-op
	}
	return append(s.rebindSlot(screenSlot, peer), s.screenRoster()...)
}

// pullFromShare removes a peer from the preview pool and, if it is the LIVE share, vacates the
// "screen" slot (placeholder, NO auto-advance, D-21). Returns the slot vacate frames (to the
// /s/screen source) + the host-only screen-roster; the CALLER re-broadcasts the participant roster
// (so the screenShare fold reflects the change). Used by the force-no-share, eligibility-revoke, and
// (indirectly) leave paths. A no-op when the peer is neither in the pool nor the live share. NOTE:
// leave() does NOT use this — it vacates occupied slots itself; this would double-vacate.
func (s *roomState) pullFromShare(id PeerID) []outbound {
	inPool := s.screenPreviews[id]
	wasLive := s.screenLiveID() == id
	if !inPool && !wasLive {
		return nil
	}
	delete(s.screenPreviews, id)
	var out []outbound
	if wasLive {
		out = append(out, s.vacateSlot(screenSlot)...)
	}
	return append(out, s.screenRoster()...)
}
