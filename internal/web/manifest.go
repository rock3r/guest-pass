package web

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// loadManifest reads the build manifest emitted by cmd/build (dir/manifest.json), which
// maps each emitted bundle name to its Subresource Integrity hash (CONVENTIONS §3.5).
// It returns nil when the manifest is absent (e.g. before a build), so templates simply
// omit the integrity attribute rather than failing.
func loadManifest(dir string) map[string]string {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}
