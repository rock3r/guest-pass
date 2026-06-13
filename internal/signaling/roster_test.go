package signaling

import "testing"

// framesTo returns every outbound addressed to a given peer, in order.
func framesTo(out []outbound, to PeerID) []Frame {
	var fs []Frame
	for _, o := range out {
		if o.to == to {
			fs = append(fs, o.frame)
		}
	}
	return fs
}

// firstFrameOfType returns the first outbound to `to` with frame type t, or false.
func firstFrameOfType(out []outbound, to PeerID, t string) (Frame, bool) {
	for _, f := range framesTo(out, to) {
		if f.T == t {
			return f, true
		}
	}
	return Frame{}, false
}

func rosterIDs(f Frame) map[string]string {
	m := map[string]string{}
	for _, e := range f.Peers {
		m[e.ID] = e.Role
	}
	return m
}

// A joining participant receives the current roster (its projection); existing
// participants are told of the newcomer via peer-joined (EN-8 / AC-2).
func TestJoinSendsRosterAndPeerJoined(t *testing.T) {
	s := newRoomState()
	s.join("host", "host") // first participant; roster has only itself

	out := s.join("g1", "guest")

	// The guest's own roster includes the host (and itself).
	roster, ok := firstFrameOfType(out, "g1", "roster")
	if !ok {
		t.Fatalf("guest should receive a roster on join, got %+v", out)
	}
	ids := rosterIDs(roster)
	if ids["host"] != "host" || ids["g1"] != "guest" {
		t.Fatalf("guest roster = %v, want host+g1", ids)
	}
	// The host is told a peer joined.
	pj, ok := firstFrameOfType(out, "host", "peer-joined")
	if !ok || pj.Peer == nil || pj.Peer.ID != "g1" || pj.Peer.Role != "guest" {
		t.Fatalf("host should get peer-joined(g1, guest), got %+v", out)
	}
}

// EN-8: the OBS source virtual peers are host-only. A guest's roster (and its
// peer-joined feed) must omit obs/obs_screen peers; the host's must include them.
func TestRosterProjectionHidesSourceFromGuest(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs")
	s.join("host", "host")
	s.join("g1", "guest")

	hostRoster := s.rosterFor("host")
	guestRoster := s.rosterFor("guest")
	if _, found := projectionHas(hostRoster, "src"); !found {
		t.Fatalf("host roster must include the obs source peer, got %v", hostRoster)
	}
	if _, found := projectionHas(guestRoster, "src"); found {
		t.Fatalf("guest roster must NOT include the obs source peer, got %v", guestRoster)
	}
	// And both rosters include the host + guest participants.
	if _, ok := projectionHas(guestRoster, "host"); !ok {
		t.Fatalf("guest roster must include the host, got %v", guestRoster)
	}
}

// A guest joining must NOT generate a peer-joined to an obs source (sources are minimal,
// EN-13) nor to a guest about an obs peer.
func TestObsSourceReceivesNoRosterFrames(t *testing.T) {
	s := newRoomState()
	s.join("host", "host")
	out := s.join("src", "obs")
	// The obs source itself gets no roster (it is a minimal source page, EN-13).
	if fs := framesTo(out, "src"); len(fs) != 0 {
		t.Fatalf("obs source should get no roster/peer frames on join, got %+v", fs)
	}
	// Joining a guest afterwards must not push peer-joined to the obs source.
	out2 := s.join("g1", "guest")
	for _, f := range framesTo(out2, "src") {
		if f.T == "peer-joined" || f.T == "roster" {
			t.Fatalf("obs source must not receive %q frames", f.T)
		}
	}
}

// Leaving broadcasts peer-left to the remaining participants (projected) (AC-2).
func TestLeaveBroadcastsPeerLeft(t *testing.T) {
	s := newRoomState()
	s.join("host", "host")
	s.join("g1", "guest")

	out := s.leave("g1")
	pl, ok := firstFrameOfType(out, "host", "peer-left")
	if !ok || pl.PeerID != "g1" {
		t.Fatalf("host should get peer-left(g1), got %+v", out)
	}
}

// A reconnect (EN-16 eviction → re-join with the same id) must NOT re-announce the peer
// to others — it never left room state, so a fresh peer-joined would desync their roster.
// The reconnecting connection still receives a fresh roster of the current state.
func TestRejoinSuppressesDuplicatePeerJoined(t *testing.T) {
	s := newRoomState()
	s.join("host", "host")
	s.join("g1", "guest") // host already learned of g1 here

	out := s.join("g1", "guest") // same id reconnects
	if _, ok := firstFrameOfType(out, "host", "peer-joined"); ok {
		t.Fatalf("a rejoin must not re-announce the peer to others, got %+v", out)
	}
	if _, ok := firstFrameOfType(out, "g1", "roster"); !ok {
		t.Fatalf("the rejoining connection should still receive a fresh roster, got %+v", out)
	}
}

// projectionHas reports whether a roster projection contains a peer id, and its role.
func projectionHas(entries []RosterEntry, id string) (string, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e.Role, true
		}
	}
	return "", false
}
