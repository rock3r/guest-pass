package web

import (
	"net/http"
	"strings"
)

// defaultMaxRequestBodyBytes is the fallback global request-body cap (1 MiB) used when the
// router is wired without an explicit limit. The production limit is config-backed
// (MAX_REQUEST_BODY_BYTES, D-M5.5-4) and validated positive at load.
const defaultMaxRequestBodyBytes = 1 << 20

// RequestBodyLimit caps the request body on every non-streaming route (D-M5.5-4 / AC-8). A
// request whose declared Content-Length exceeds the cap is rejected outright with 413 and the
// wrapped handler never runs; for a chunked/unknown-length body the cap is enforced via
// http.MaxBytesReader — every read of r.Body goes through the capped reader, so no handler can
// buffer an oversized body into memory regardless of whether it maps the read error to 413.
//
// Only a genuine WebSocket upgrade to /ws is exempt — that body is the hijacked streaming socket,
// not a finite request body, and capping it would corrupt the stream. The exemption is gated on
// the upgrade handshake (not the path alone), so a stray POST /ws or a non-upgrade GET /ws is
// still capped like any other route.
func RequestBodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ws" && isWebSocketUpgrade(r) { // streaming signaling — exempt (D-M5.5-4)
				next.ServeHTTP(w, r)
				return
			}
			// Fast path: a declared length over the cap is rejected without reading the body.
			if r.ContentLength > maxBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			// Enforce the cap on the bytes actually read (covers chunked/unknown-length too):
			// a read past the limit fails, so the handler can never buffer an oversized body.
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// isWebSocketUpgrade reports whether r is a WebSocket upgrade handshake: a GET carrying
// `Connection: Upgrade` (a comma-listed, case-insensitive token) and `Upgrade: websocket`. This
// mirrors the upgrade preconditions the /ws handler itself enforces, so the body-cap exemption
// can't be reached by a non-streaming request that merely shares the path.
func isWebSocketUpgrade(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, tok := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
			return true
		}
	}
	return false
}
