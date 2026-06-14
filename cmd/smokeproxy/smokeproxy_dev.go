//go:build dev

// smokeproxy is a dev-only path-allowlist reverse proxy for the manual smoke (scripts/smoke.sh).
//
// The public HTTPS tunnel points at THIS proxy instead of the server directly. It forwards ONLY the
// guest journey (/p, /ws, /static) to the local server and refuses everything else — so the
// admin-granting /auth/dev, the host greenroom, and the admin API are NOT reachable over the tunnel
// even though a guest link necessarily reveals the tunnel origin (a guest could otherwise trim their
// /p/<token> URL to /auth/dev; the tunnel forwards from localhost, so the server's loopback-only
// dev-login guard would mint a host/admin session). OBS source pages (/s/) are deliberately NOT
// allowed: their slot source token is a crown-jewel credential (EN-5), so devsmoke prints OBS source
// URLs on loopback and the host runs OBS on this machine — the token never traverses the tunnel. (To
// drive OBS from a separate machine, re-allow "/s/" here and accept that the source token then rides
// the tunnel.) The host uses http://localhost:8137 directly (loopback, full access). SMOKE-ONLY
// safety shim, never a deployment proxy — compiled only under `-tags dev` (a release build is an
// inert stub so `go build ./...` still compiles the package).
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// allowedOverTunnel reports whether a request path is part of the guest journey (page + signaling +
// static assets) that is safe to expose over the public tunnel. Everything else — the landing +
// sign-in, /auth/*, /greenroom, /api/*, and the OBS source pages /s/ (crown-jewel source token) — is
// refused, so the tunnel can reach neither a host/admin capability nor a slot source credential.
func allowedOverTunnel(p string) bool {
	switch {
	case p == "/ws": // signaling WebSocket — over the tunnel this carries guest signaling (?pass=); the host + OBS use it on loopback
		return true
	case p == "/healthz":
		return true
	case strings.HasPrefix(p, "/p/"): // guest magic-link page + the /p/{token}/enter action
		return true
	case strings.HasPrefix(p, "/static/"): // bundled JS/CSS/fonts
		return true
	default:
		return false
	}
}

func main() {
	listen := flag.String("listen", ":8138", "address the public tunnel points at")
	target := flag.String("target", "http://localhost:8137", "the local guestpass server to forward guest routes to")
	flag.Parse()

	u, err := url.Parse(*target)
	if err != nil {
		log.Fatalf("smokeproxy: bad --target %q: %v", *target, err)
	}
	// NewSingleHostReverseProxy forwards to the local server and (since Go 1.12) transparently
	// proxies the /ws WebSocket upgrade. It preserves the incoming Host header, so the server's
	// same-origin /ws check still sees the tunnel origin on both Host and Origin.
	rp := httputil.NewSingleHostReverseProxy(u)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedOverTunnel(r.URL.Path) {
			http.Error(w, "not available over the smoke tunnel — host/admin routes are loopback-only", http.StatusNotFound)
			return
		}
		rp.ServeHTTP(w, r)
	})

	log.Printf("smokeproxy: listening on %s, forwarding GUEST routes only to %s (host/admin routes blocked)", *listen, *target)
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
