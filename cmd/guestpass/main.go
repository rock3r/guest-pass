// Command guestpass is the GuestPass server — a single static Go binary
// (signaling relay + SQLite + embedded frontend & OBS source pages).
// See docs/ARCHITECTURE.md for the design.
package main

import (
	"fmt"

	"github.com/rock3r/guest-pass/internal/buildinfo"
)

func main() {
	fmt.Printf("guestpass %s (%s)\n", buildinfo.Version(), buildinfo.Commit())
	fmt.Printf("source: %s\n", buildinfo.SourceURL())
}
