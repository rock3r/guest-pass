package signaling

import "testing"

// AC-8/D-19: the program quality ceiling is broadcast to every PUBLISHING participant (guest +
// co-host + host) as a {t:ceiling} frame so each caps its program encoder; OBS source pages (which
// don't publish) never receive it.
func TestSetCeilingBroadcastsToParticipants(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("co", "cohost", "")
	s.join("src", "obs", "")

	out := s.setCeiling(720, 30, 2500)
	for _, id := range []PeerID{"host", "g1", "co"} {
		f, ok := firstFrameOfType(out, id, "ceiling")
		if !ok {
			t.Fatalf("participant %q did not receive the ceiling", id)
		}
		if f.MaxRes != 720 || f.MaxFps != 30 || f.MaxBitrateKbps != 2500 {
			t.Fatalf("ceiling to %q = %d/%d/%d, want 720/30/2500", id, f.MaxRes, f.MaxFps, f.MaxBitrateKbps)
		}
	}
	if hasFrameOfType(framesToWrap(out, "src"), "ceiling") {
		t.Fatalf("an OBS source must not receive the ceiling (it doesn't publish)")
	}
}

// AC-8/D-19: a per-source program-resolution override ({t:source-quality} carrying ?res) is relayed
// from the OBS source to its slot's bound occupant, stamped with the source's id so the occupant
// caps the sender feeding THAT source. Only the occupant receives it.
func TestSourceQualityRelaysToOccupant(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "")
	s.rebindSlot("cam-1", "g1")

	out := s.sourceQuality("src", 480)
	f, ok := firstFrameOfType(out, "g1", "source-quality")
	if !ok {
		t.Fatalf("the bound occupant did not receive the per-source override, got %+v", out)
	}
	if f.PeerID != "src" || f.Res != 480 {
		t.Fatalf("source-quality to occupant = source %q res %d, want src/480", f.PeerID, f.Res)
	}
	// No one else receives it.
	for _, o := range out {
		if o.to != "g1" {
			t.Fatalf("source-quality leaked to %q, want only the occupant", o.to)
		}
	}
}

// sourceQuality on a source whose slot is UNBOUND (no occupant) emits nothing — there is no
// publisher to cap.
func TestSourceQualityUnboundSlotNoop(t *testing.T) {
	s := newRoomState()
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	if out := s.sourceQuality("src", 480); out != nil {
		t.Fatalf("source-quality on an unbound slot must be a no-op, got %+v", out)
	}
	// A source-quality from a peer that sources no slot is also a no-op (a guest can't spoof it).
	s.join("g1", "guest", "")
	if out := s.sourceQuality("g1", 480); out != nil {
		t.Fatalf("source-quality from a non-source peer must be a no-op, got %+v", out)
	}
}

// framesToWrap returns the outbounds addressed to `to` as a []outbound (so hasFrameOfType, which
// takes []outbound, can be reused for a per-recipient check).
func framesToWrap(out []outbound, to PeerID) []outbound {
	var r []outbound
	for _, o := range out {
		if o.to == to {
			r = append(r, o)
		}
	}
	return r
}
