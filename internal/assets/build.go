// Package assets builds the GuestPass frontend with esbuild used as a Go library — no
// node, no npm, no package.json (D-32). It is shared by cmd/build (the CLI / watch) and
// the browser test harness (internal/browsertest) so both produce byte-identical bundles
// from one pinned config.
//
// Two DELIBERATELY separate bundles are emitted (AD-7, EN-13):
//
//   - app: the device-check + guest-session + greenroom islands (fonts + full CSS).
//   - obs: the cam + screen OBS source pages (no fonts, minimal JS).
//
// Vendored Preact (web/vendor/preact, MIT) is resolved via an Alias map so no bare
// node_modules resolution is ever attempted; adding a Preact sub-path requires adding
// BOTH the vendored file and its alias here.
package assets

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/evanw/esbuild/pkg/api"
)

// manifestBundles are the entry bundles whose Subresource Integrity hashes templates
// inject (CONVENTIONS §3.5). Chunks/sourcemaps are not referenced directly by HTML.
var manifestBundles = []string{"app.css", "app.js", "obs.js"}

// vendoredPreact maps the bare Preact specifiers to the exact committed files.
var vendoredPreact = map[string]string{
	"preact":             "web/vendor/preact/preact.module.js",
	"preact/hooks":       "web/vendor/preact/hooks.module.js",
	"preact/jsx-runtime": "web/vendor/preact/jsx-runtime.module.js",
}

// guestEngines pins CSS/JS down-level to the oldest supported GUEST browsers — NOT OBS's
// CEF (EN-13), since the guest joins from whatever device they own.
var guestEngines = []api.Engine{
	{Name: api.EngineChrome, Version: "109"},
	{Name: api.EngineFirefox, Version: "109"},
	{Name: api.EngineSafari, Version: "15.6"},
	{Name: api.EngineEdge, Version: "109"},
}

// BuildOptions returns the esbuild config that compiles web/src under root into outDir.
// root must be the repo root: it anchors the source entry points and the vendored-Preact
// alias (which esbuild requires as absolute paths, since a bare alias value is treated as
// a package specifier). outDir receives the bundles + the SRI manifest.
func BuildOptions(root, outDir string) api.BuildOptions {
	alias := make(map[string]string, len(vendoredPreact))
	for spec, rel := range vendoredPreact {
		alias[spec] = filepath.Join(root, rel)
	}
	return api.BuildOptions{
		AbsWorkingDir: root,
		Plugins: []api.Plugin{{
			// Regenerate the SRI manifest after every successful build — including each
			// --watch rebuild — so rendered pages never serve a stale/missing integrity.
			Name: "sri-manifest",
			Setup: func(b api.PluginBuild) {
				b.OnEnd(func(r *api.BuildResult) (api.OnEndResult, error) {
					if len(r.Errors) > 0 {
						return api.OnEndResult{}, nil
					}
					return api.OnEndResult{}, writeManifest(outDir)
				})
			},
		}},
		EntryPoints: []string{
			"web/src/islands/app.js",
			"web/src/obs/obs.js",
			"web/src/spike2/guest.js",
			"web/src/spike2/control.js",
		},
		Outdir:           outDir,
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

// Build compiles the frontend bundles under root into outDir (a one-shot build). The SRI
// manifest is written by the sri-manifest OnEnd plugin. It returns an error listing every
// esbuild error on failure.
func Build(root, outDir string) error {
	result := api.Build(BuildOptions(root, outDir))
	if len(result.Errors) > 0 {
		msgs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			msgs = append(msgs, e.Text)
		}
		return fmt.Errorf("assets: build failed: %v", msgs)
	}
	return nil
}

// writeManifest computes the SRI hash (sha384, the SRI-recommended digest) of each emitted
// entry bundle in outDir and writes outDir/manifest.json mapping bundle name to
// "sha384-<base64>". The web layer reads this to add integrity= attributes.
func writeManifest(outDir string) error {
	m := map[string]string{}
	for _, name := range manifestBundles {
		b, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			continue // bundle not emitted (e.g. no CSS imported) — skip it
		}
		sum := sha512.Sum384(b)
		m[name] = "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "manifest.json"), append(out, '\n'), 0o644)
}
