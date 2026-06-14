//go:build !dev

// smokeproxy is a dev-only smoke helper; the release build is an inert stub so that a plain
// `go build ./...` still compiles this package. Build it with `-tags dev` to use it.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "smokeproxy is a dev-only tool — rebuild with `-tags dev`")
	os.Exit(1)
}
