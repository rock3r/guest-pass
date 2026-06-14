package signaling

import "testing"

// statsOf returns a peer's signal + degraded as projected in the roster delivered to `to`.
func statsOf(out []outbound, to, peer PeerID) (int, *DegradedView) {
	e, _ := rosterEntryFor(out, to, peer)
	return e.Signal, e.Degraded
}

// T-13 / AC-14: a publisher's {t:stats} self-report folds signal + degraded into its roster entry,
// visible to everyone (incl. its own self entry); a report that doesn't change signal/degraded is a
// no-op (no roster churn — rttMs alone, which ticks every sample, must not spam the roster).
func TestApplyStatsFoldsSignalAndDegraded(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")

	out := s.applyStats("g1", 3, 80, &DegradedView{Dir: "lowering", Reason: "cpu"})
	if p := s.peers["g1"]; p.signal != 3 || p.rttMs != 80 || p.degraded == nil || p.degraded.Reason != "cpu" {
		t.Fatalf("g1 stats not stored: %+v", p)
	}
	sig, deg := statsOf(out, "host", "g1")
	if sig != 3 || deg == nil || deg.Dir != "lowering" || deg.Reason != "cpu" {
		t.Fatalf("roster to host must reflect g1 signal=3 degraded=lowering/cpu, got sig=%d deg=%+v", sig, deg)
	}
	if selfSig, _ := statsOf(out, "g1", "g1"); selfSig != 3 {
		t.Fatalf("g1's own self entry must reflect signal=3, got %d", selfSig)
	}

	// Same signal + same degraded, only rttMs moved (80→95): no rebroadcast.
	if out := s.applyStats("g1", 3, 95, &DegradedView{Dir: "lowering", Reason: "cpu"}); out != nil {
		t.Fatalf("an unchanged signal/degraded report must not rebroadcast, got %+v", out)
	}

	// Recovery: signal rises and degraded clears (nil) → rebroadcast reflects it.
	out = s.applyStats("g1", 5, 40, nil)
	if p := s.peers["g1"]; p.degraded != nil {
		t.Fatalf("g1 degraded should be cleared, got %+v", p.degraded)
	}
	if sig, deg := statsOf(out, "host", "g1"); sig != 5 || deg != nil {
		t.Fatalf("roster must reflect recovery signal=5 degraded=nil, got sig=%d deg=%+v", sig, deg)
	}
}

// A direction/reason change (lowering→recovering) is a material change → rebroadcast.
func TestApplyStatsDirectionChangeRebroadcasts(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyStats("g1", 2, 120, &DegradedView{Dir: "lowering", Reason: "bandwidth"})

	out := s.applyStats("g1", 2, 120, &DegradedView{Dir: "recovering", Reason: "bandwidth"})
	if _, deg := statsOf(out, "host", "g1"); deg == nil || deg.Dir != "recovering" {
		t.Fatalf("a lowering→recovering change must rebroadcast as recovering, got %+v", deg)
	}
}

// Stats from a non-participant (unknown id / OBS source virtual peer) are a no-op (EN-7/EN-13).
func TestApplyStatsIgnoresNonParticipant(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	if out := s.applyStats("ghost", 3, 50, nil); out != nil {
		t.Fatalf("stats from a non-participant must be a no-op, got %+v", out)
	}
}
