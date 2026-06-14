//go:build dev

// smokeproxy is a dev-only path-allowlist reverse proxy for the manual smoke (scripts/smoke.sh).
//
// The public HTTPS tunnel points at THIS proxy instead of the server directly. It forwards ONLY the
// guest / OBS-source routes to the local server and refuses everything else — so the admin-granting
// /auth/dev, the host greenroom, and the admin API are NOT reachable over the tunnel even though a
// guest link necessarily reveals the tunnel origin (a guest could otherwise trim their /p/<token>
// URL to /auth/dev; the tunnel forwards from localhost, so the server's loopback-only dev-login guard
// would mint a host/admin session). The host uses http://localhost:8137 directly (loopback, full
// access). This is a SMOKE-ONLY safety shim, never a deployment proxy — compiled only under `-tags
// dev` (a release build is an inert stub so `go build ./...` still compiles the package).
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

// allowedOverTunnel reports whether a request path is part of the guest / OBS-source / signaling
// surface that is safe to expose over the public tunnel. Everything else (the landing + sign-in,
// /auth/*, /greenroom, /api/*, …) is refused so the tunnel can't reach a host/admin capability.
func allowedOverTunnel(p string) bool {
	switch {
	case p == "/ws": // signaling WebSocket — guests (?pass=) and OBS sources (?src=) only; host uses loopback
		return true
	case p == "/healthz":
		return true
	case strings.HasPrefix(p, "/p/"): // guest magic-link page + the /p/{token}/enter action
		return true
	case strings.HasPrefix(p, "/s/"): // OBS source pages
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
