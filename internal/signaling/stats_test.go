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

// T-14 / AC-15: a host "bump quality now" broadcasts {t:recover-quality} to every PARTICIPANT
// (publisher) so each recovers locally; OBS source virtual peers don't publish and don't receive it.
func TestRecoverQualityBroadcastsToParticipants(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("co", "cohost", "")

	got := map[PeerID]bool{}
	for _, o := range s.recoverQuality() {
		if o.frame.T != "recover-quality" {
			t.Fatalf("unexpected frame %q in the recover-quality broadcast", o.frame.T)
		}
		got[o.to] = true
	}
	for _, id := range []PeerID{"host", "g1", "co"} {
		if !got[id] {
			t.Fatalf("recover-quality must reach participant %q, got %+v", id, got)
		}
	}
}

// AC-15 (M3 plan default): degradation transparency reaches the HOST and CO-HOST (read-only — only
// the host has the quality controls), but a plain GUEST sees only its OWN — another guest's health
// is stripped from a guest's projection (the data never reaches a guest, not merely hidden client-side).
func TestApplyStatsTransparencyHostAndCohostNotGuest(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("co", "cohost", "")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")
	out := s.applyStats("g1", 2, 90, &DegradedView{Dir: "lowering", Reason: "cpu"})

	if sig, deg := statsOf(out, "host", "g1"); sig != 2 || deg == nil {
		t.Fatalf("the host must see g1's signal+degraded, got sig=%d deg=%+v", sig, deg)
	}
	if sig, deg := statsOf(out, "co", "g1"); sig != 2 || deg == nil {
		t.Fatalf("a co-host must see g1's signal+degraded (read-only transparency), got sig=%d deg=%+v", sig, deg)
	}
	if sig, deg := statsOf(out, "g1", "g1"); sig != 2 || deg == nil {
		t.Fatalf("g1 must see its OWN signal+degraded, got sig=%d deg=%+v", sig, deg)
	}
	if sig, deg := statsOf(out, "g2", "g1"); sig != 0 || deg != nil {
		t.Fatalf("a plain guest must NOT see another guest's degradation (AC-15), got sig=%d deg=%+v", sig, deg)
	}
}
