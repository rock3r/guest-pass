package signaling

import (
	"strings"
	"unicode"
)

// maxDisplayNameLen bounds a nameplate display name in runes (EN-15). A nameplate is a single
// short overlay line; this keeps a hostile or accidental over-long name from blowing out the OBS
// layout. It counts runes, not bytes, so a name of accented or non-Latin letters isn't unfairly
// clipped mid-character.
const maxDisplayNameLen = 60

// CapDisplayName sanitizes a display name for the nameplate (EN-15 charset/length cap). It strips
// control and non-printable runes — so a hostile name can't smuggle newlines, NULs, or zero-width
// tricks into the OBS overlay — trims surrounding whitespace, and caps the result to
// maxDisplayNameLen runes. The OBS source still renders the value as escaped textContent (the
// injection-safe half of EN-15); this is the charset/length half. It is PURE and IDEMPOTENT, so it
// can run at every name-write boundary (room join, host override, and the persisted DB write)
// without the boundaries ever disagreeing on the canonical form.
func CapDisplayName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1 // drop controls, zero-width, non-ASCII spaces, and other non-printables
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	if runes := []rune(cleaned); len(runes) > maxDisplayNameLen {
		// Truncation can leave a dangling space (e.g. cut mid-"word boundary"); re-trim so the
		// capped name never ends in whitespace.
		return strings.TrimSpace(string(runes[:maxDisplayNameLen]))
	}
	return cleaned
}
