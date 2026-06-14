//go:build !dev

// devsmoke is a dev-only manual-smoke helper; the release build is an inert stub so that a
// plain `go build ./...` still compiles this package. Build it with `-tags dev` to use it.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "devsmoke is a dev-only tool — rebuild with `-tags dev`")
	os.Exit(1)
}
