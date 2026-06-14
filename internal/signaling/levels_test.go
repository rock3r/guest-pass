package signaling

import (
	"encoding/json"
	"strings"
	"testing"
)

// levelsFrameTo returns the {t:levels} frame addressed to `to`, or false.
func levelsFrameTo(out []outbound, to PeerID) (Frame, bool) {
	return firstFrameOfType(out, to, "levels")
}

// T-2 (AD-13): per-peer audio levels batch onto ONE {t:levels} tick frame per participant —
// the full participant level map — not the roster. OBS source virtual peers have no meter and
// neither appear in the map nor receive the frame (EN-13).
func TestBuildLevelsBatchesParticipants(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")
	s.join("src", "obs", "")
	s.applyState("g1", nil, nil, nil, fptr(0.4))
	s.applyState("g2", nil, nil, nil, fptr(0.7))

	out := s.buildLevels()

	// Each participant gets the batched map of ALL participants' levels.
	for _, to := range []PeerID{"host", "g1", "g2"} {
		f, ok := levelsFrameTo(out, to)
		if !ok {
			t.Fatalf("%s should receive a {t:levels} tick, got %+v", to, out)
		}
		if f.Levels["g1"] != 0.4 || f.Levels["g2"] != 0.7 {
			t.Fatalf("%s levels = %+v, want g1=0.4 g2=0.7", to, f.Levels)
		}
		// The host (no reported level) is present at 0 — meters need a value for every tile.
		if _, ok := f.Levels["host"]; !ok {
			t.Fatalf("the level map must include every participant, got %+v", f.Levels)
		}
		// The OBS source has no meter: it never appears in the map.
		if _, ok := f.Levels["src"]; ok {
			t.Fatalf("an OBS source must not appear in the level map, got %+v", f.Levels)
		}
	}
	// And the OBS source receives no levels tick of its own (EN-13).
	if _, ok := levelsFrameTo(out, "src"); ok {
		t.Fatalf("an OBS source must not receive a {t:levels} tick (EN-13)")
	}
}

// AD-13: the tick stays SILENT in a quiet room (no idle spam), but emits ONE trailing all-zero
// frame when the room falls silent so clients settle their meters, then nothing until sound
// returns.
func TestBuildLevelsSilentGating(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")

	// Quiet from the start: no tick.
	if out := s.buildLevels(); out != nil {
		t.Fatalf("a quiet room must not emit a levels tick, got %+v", out)
	}

	// Sound: the tick emits.
	s.applyState("g1", nil, nil, nil, fptr(0.5))
	if out := s.buildLevels(); len(out) == 0 {
		t.Fatalf("a room with sound must emit a levels tick")
	}

	// Silence again: exactly ONE trailing all-zero frame, then quiet.
	s.applyState("g1", nil, nil, nil, fptr(0))
	trailing := s.buildLevels()
	if f, ok := levelsFrameTo(trailing, "g1"); !ok || f.Levels["g1"] != 0 {
		t.Fatalf("falling silent must emit one trailing all-zero frame, got %+v", trailing)
	}
	if out := s.buildLevels(); out != nil {
		t.Fatalf("after the trailing frame a quiet room must go silent again, got %+v", out)
	}
}

// EN-11/AD-13: a reported level rides ONLY the {t:levels} tick — never the roster. The roster
// entry shape has no level field at all, and a level update on its own never re-broadcasts the
// roster, so an audio meter cannot leak into roster traffic.
func TestLevelsNeverInRoster(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")

	// A meter-only update emits no roster frame.
	if out := s.applyState("g1", nil, nil, nil, fptr(0.8)); out != nil {
		t.Fatalf("a level update must not produce a roster frame, got %+v", out)
	}

	// Even a roster re-broadcast (here from a presence change) carries no level: the {t:roster}
	// frame has no levels map, and no entry serializes a level field.
	out := s.applyState("g1", bptr(true), nil, nil, nil)
	rf, ok := firstFrameOfType(out, "host", "roster")
	if !ok {
		t.Fatalf("a presence change should re-broadcast the roster, got %+v", out)
	}
	if rf.Levels != nil {
		t.Fatalf("a roster frame must not carry a levels map, got %+v", rf.Levels)
	}
	b, _ := json.Marshal(rf)
	if strings.Contains(string(b), "level") {
		t.Fatalf("a roster frame must not serialize any level, got %s", b)
	}
}
