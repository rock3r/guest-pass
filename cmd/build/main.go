// Command build compiles the GuestPass frontend into web/dist using the shared
// internal/assets esbuild config — no node, no npm, no package.json (D-32).
// Run: go run ./cmd/build [--watch]. Must be invoked from the repo root.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/rock3r/guest-pass/internal/assets"
	"github.com/rock3r/guest-pass/internal/guide"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
	dist := filepath.Join(root, "web", "dist")

	if len(os.Args) > 1 && os.Args[1] == "--watch" {
		ctx, err := api.Context(assets.BuildOptions(root, dist))
		if err != nil {
			fmt.Fprintln(os.Stderr, "esbuild:", err)
			os.Exit(1)
		}
		if err := ctx.Watch(api.WatchOptions{}); err != nil {
			fmt.Fprintln(os.Stderr, "esbuild watch:", err)
			os.Exit(1)
		}
		fmt.Println("esbuild watching web/src … ctrl-c to stop")
		select {} // block forever
	}

	if err := assets.Build(root, dist); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := guide.Compile(root, dist); err != nil {
		fmt.Fprintln(os.Stderr, "guide:", err)
		os.Exit(1)
	}
	fmt.Println("built web/dist (app + obs entries + user guide) + SRI manifest")
}
