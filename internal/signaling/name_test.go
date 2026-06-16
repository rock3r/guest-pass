package signaling

import (
	"strings"
	"testing"
)

// EN-15: the nameplate display name is rendered as escaped textContent AND passed through a
// server-side charset/length cap. CapDisplayName is that cap: it trims, strips control/non-printable
// runes (so a hostile name can't smuggle newlines/zero-width tricks into the overlay), and bounds
// the length. It is pure + idempotent so it can run at every name boundary without divergence.
func TestCapDisplayName(t *testing.T) {
	long := strings.Repeat("á", maxDisplayNameLen+40)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Greta", "Greta"},
		{"trims", "  Greta  ", "Greta"},
		{"empty stays empty", "", ""},
		{"whitespace-only collapses to empty", "   \t\n ", ""},
		{"keeps internal ascii space", "Greta Garbo", "Greta Garbo"},
		{"keeps unicode letters", "Renée", "Renée"},
		{"strips newlines", "Greta\nGarbo", "GretaGarbo"},
		{"strips tabs and controls", "Gr\tet\x00a", "Greta"},
		{"strips zero-width", "Gr\u200beta", "Greta"},
		{"caps length", long, strings.Repeat("á", maxDisplayNameLen)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CapDisplayName(c.in)
			if got != c.want {
				t.Fatalf("CapDisplayName(%q) = %q, want %q", c.in, got, c.want)
			}
			// Idempotent: capping a capped name changes nothing.
			if again := CapDisplayName(got); again != got {
				t.Fatalf("CapDisplayName not idempotent: %q → %q", got, again)
			}
			// Never exceeds the rune cap.
			if n := len([]rune(got)); n > maxDisplayNameLen {
				t.Fatalf("CapDisplayName(%q) length %d exceeds cap %d", c.in, n, maxDisplayNameLen)
			}
		})
	}
}

// A name override is capped at the room boundary too (EN-15): a hostile over-long/control-laden
// name set via setName reaches the source already trimmed of control chars and length-bounded, so
// the OBS overlay can never receive a smuggled newline even if the web cap were bypassed.
func TestSetNameCapsAtBoundary(t *testing.T) {
	s := newRoomState()
	s.join("host", "host", "")
	s.join("src", "obs", "")
	s.attachSource("cam-1", "src")
	s.join("g1", "guest", "Greta")
	s.rebindSlot("cam-1", "g1")

	out := s.setName("g1", "Mar\ngaret\x00")
	f, ok := firstFrameOfType(out, "src", "slot-rebind")
	if !ok {
		t.Fatalf("setName must re-send slot-rebind, got %+v", out)
	}
	if f.Name != "Margaret" {
		t.Fatalf("nameplate name = %q, want the control chars stripped to Margaret", f.Name)
	}
}
