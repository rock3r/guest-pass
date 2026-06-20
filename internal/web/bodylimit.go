package web

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// defaultMaxRequestBodyBytes is the fallback global request-body cap (1 MiB) used when the
// router is wired without an explicit limit. The production limit is config-backed
// (MAX_REQUEST_BODY_BYTES, D-M5.5-4) and validated positive at load.
const defaultMaxRequestBodyBytes = 1 << 20

// RequestBodyLimit caps the request body on every non-streaming route (D-M5.5-4 / AC-8) and
// rejects an oversized one with 413 BEFORE the handler runs — so even a handler that ignores the
// body (an admin action keyed on the URL, /p/{token}/enter, …) can't execute on an oversized
// request. It handles the two body shapes:
//   - Known length (Content-Length): over the cap → 413 outright; within the cap → wrapped in
//     http.MaxBytesReader as belt-and-suspenders on the bytes actually read.
//   - Unknown length (chunked / Transfer-Encoding): the size isn't known up front, so the body is
//     buffered up to the cap here; over the cap → 413 before dispatch, within the cap → re-provided
//     to the handler intact. GuestPass has no streaming-upload route, so this never truncates a
//     legitimate body, and the buffer is bounded by the cap (no worse than a Content-Length body).
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
			// Declared length over the cap: reject without reading the body.
			if r.ContentLength > maxBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			if r.ContentLength < 0 {
				// Unknown length: buffer up to the cap so an oversized chunked body is rejected here,
				// not silently accepted by a handler that never reads it. Reading cap+1 bytes proves
				// "over the cap" without buffering the whole (potentially unbounded) stream.
				body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
				_ = r.Body.Close()
				if err != nil {
					http.Error(w, "error reading request body", http.StatusBadRequest)
					return
				}
				if int64(len(body)) > maxBytes {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body)) // now a known, within-cap length for the handler
				next.ServeHTTP(w, r)
				return
			}
			// Known length within the cap: enforce on the bytes actually read (belt-and-suspenders).
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
