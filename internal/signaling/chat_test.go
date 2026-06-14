package signaling

import "testing"

// AC-6/T-6: a backstage chat relays to every PARTICIPANT (including the sender), stamped with
// the sender's id; OBS source virtual peers never receive it (EN-13).
func TestRelayChatToParticipantsFromStamped(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("g2", "guest", "")
	s.join("src", "obs", "")

	out := s.relayChat("g1", "hello backstage")

	got := map[PeerID]Frame{}
	for _, o := range out {
		got[o.to] = o.frame
	}
	for _, who := range []PeerID{"host", "g1", "g2"} {
		f, ok := got[who]
		if !ok {
			t.Fatalf("%s should receive the chat (sender included), got %+v", who, out)
		}
		if f.T != "chat" || f.From != "g1" || f.Text != "hello backstage" {
			t.Fatalf("%s chat = %+v, want {chat, from:g1, text:hello backstage}", who, f)
		}
	}
	if _, ok := got["src"]; ok {
		t.Fatalf("an OBS source must not receive backstage chat (EN-13)")
	}
}

// EN-7: the `from` is stamped from the sender's authenticated id — a client cannot spoof it,
// and a chat from a non-participant (OBS source) or an unknown peer is dropped.
func TestRelayChatStampsFromAndDropsNonParticipant(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.join("src", "obs", "")

	// The reducer always stamps the real sender, regardless of any client-supplied value.
	out := s.relayChat("g1", "hi")
	for _, o := range out {
		if o.frame.From != "g1" {
			t.Fatalf("chat must be stamped from the sender g1, got from=%q", o.frame.From)
		}
	}
	// A chat from an OBS source is dropped (sources have no backstage chat, EN-13).
	if out := s.relayChat("src", "should not relay"); out != nil {
		t.Fatalf("an OBS source must not be able to chat, got %+v", out)
	}
	// A chat from an unknown peer is dropped.
	if out := s.relayChat("ghost", "nope"); out != nil {
		t.Fatalf("a chat from an unknown peer must be dropped, got %+v", out)
	}
}
