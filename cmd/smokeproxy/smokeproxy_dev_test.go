//go:build dev

package main

import "testing"

func TestAllowedOverTunnel_AllowsGuestAssets(t *testing.T) {
	for _, path := range []string{"/_gp/app.css", "/_gp/app.js", "/_gp/newsreader.woff2"} {
		if !allowedOverTunnel(path) {
			t.Errorf("allowedOverTunnel(%q) = false, want true", path)
		}
	}
}
