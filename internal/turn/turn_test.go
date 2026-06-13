package turn

import (
	"encoding/json"
	"testing"
)

func TestICEServers_STUNAlwaysWhenConfigured(t *testing.T) {
	got := ICEServers("stun:stun.example.org:3478")
	if len(got) != 1 {
		t.Fatalf("want 1 ICE server, got %d (%+v)", len(got), got)
	}
	s := got[0]
	if len(s.URLs) != 1 || s.URLs[0] != "stun:stun.example.org:3478" {
		t.Errorf("URLs = %v, want [stun:stun.example.org:3478]", s.URLs)
	}
	// STUN entries carry no credentials (those belong to a TURN entry, M2/EN-4).
	if s.Username != "" || s.Credential != "" {
		t.Errorf("STUN entry should have no creds, got username=%q credential=%q", s.Username, s.Credential)
	}
}

func TestICEServers_EmptyWhenNoSTUN(t *testing.T) {
	// Dev / loopback runs without a STUN server: peers reach each other on host-local
	// candidates, so the ICE config is simply empty (D-38). No relay is offered in M1.
	if got := ICEServers(""); got != nil {
		t.Errorf("no STUN_URL should yield a nil ICE server list, got %+v", got)
	}
	if got := ICEServers("   "); got != nil {
		t.Errorf("blank STUN_URL should yield a nil ICE server list, got %+v", got)
	}
}

// The marshaled shape must match the browser RTCIceServer dictionary (urls array, and
// no username/credential keys for a STUN-only entry) so the client can hand it straight
// to RTCPeerConnection.
func TestICEServers_MarshalsToRTCIceServerShape(t *testing.T) {
	b, err := json.Marshal(ICEServers("stun:stun.example.org:3478"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	want := `[{"urls":["stun:stun.example.org:3478"]}]`
	if got != want {
		t.Errorf("marshal = %s, want %s", got, want)
	}
}
