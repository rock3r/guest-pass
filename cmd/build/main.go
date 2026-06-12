// Command build compiles the GuestPass frontend with esbuild used as a Go library
// — no node, no npm, no package.json (D-32). Run: go run ./cmd/build [--watch].
//
// It emits two DELIBERATELY separate bundles into web/dist (AD-7, EN-13):
//
//   - app: the device-check + guest-session + greenroom islands (fonts + full CSS).
//   - obs: the cam + screen OBS source pages (no fonts, minimal JS).
//
// Vendored Preact (web/vendor/preact, MIT) is resolved via an Alias map so no bare
// node_modules resolution is ever attempted; adding a Preact sub-path requires
// adding BOTH the vendored file and its alias here.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/evanw/esbuild/pkg/api"
)

// vendoredPreact maps the bare Preact specifiers to the exact committed files.
var vendoredPreact = map[string]string{
	"preact":             "web/vendor/preact/preact.module.js",
	"preact/hooks":       "web/vendor/preact/hooks.module.js",
	"preact/jsx-runtime": "web/vendor/preact/jsx-runtime.module.js",
}

// guestEngines pins CSS/JS down-level to the oldest supported GUEST browsers — NOT
// OBS's CEF (EN-13), since the guest joins from whatever device they own.
//
// TODO(M1b): ratify these floors against the requirements table (D-34). They
// currently sit just below color-mix() support so esbuild lowers it — which is what
// the SPIKE-1 manual Safari check validates (esbuild's color-mix() lowering has
// known Safari bugs, EN-13).
var guestEngines = []api.Engine{
	{Name: api.EngineChrome, Version: "109"},
	{Name: api.EngineFirefox, Version: "109"},
	{Name: api.EngineSafari, Version: "15.6"},
	{Name: api.EngineEdge, Version: "109"},
}

// options builds the esbuild config. It must be invoked with the working directory
// at the repo root (e.g. `go run ./cmd/build`); alias targets are resolved to
// absolute paths because esbuild treats a bare (non-"./") alias value as a package
// specifier, not a relative file.
func options() api.BuildOptions {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
	alias := make(map[string]string, len(vendoredPreact))
	for spec, rel := range vendoredPreact {
		alias[spec] = filepath.Join(root, rel)
	}
	return api.BuildOptions{
		AbsWorkingDir: root,
		EntryPoints: []string{
			"web/src/islands/app.js",
			"web/src/obs/obs.js",
			"web/src/spike2/guest.js",
			"web/src/spike2/control.js",
		},
		Outdir:           "web/dist",
		Bundle:           true,
		Format:           api.FormatESModule,
		Splitting:        true,
		EntryNames:       "[name]",
		ChunkNames:       "chunk-[hash]",
		AssetNames:       "[name]-[hash]",
		Alias:            alias,
		JSX:              api.JSXAutomatic,
		JSXImportSource:  "preact",
		Loader:           map[string]api.Loader{".js": api.LoaderJSX},
		Engines:          guestEngines,
		MinifySyntax:     true,
		MinifyWhitespace: true,
		Sourcemap:        api.SourceMapLinked,
		Write:            true,
		LogLevel:         api.LogLevelWarning,
	}
}

func main() {
	watch := len(os.Args) > 1 && os.Args[1] == "--watch"
	if watch {
		ctx, err := api.Context(options())
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

	result := api.Build(options())
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w.Text)
	}
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "build error: %s\n", e.Text)
		}
		os.Exit(1)
	}
	fmt.Println("built web/dist (app + obs entries)")
}
