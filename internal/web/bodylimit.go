package web

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultMaxRequestBodyBytes is the fallback global request-body cap (1 MiB) used when the
// router is wired without an explicit limit. The production limit is config-backed
// (MAX_REQUEST_BODY_BYTES, D-M5.5-4) and validated positive at load.
const defaultMaxRequestBodyBytes = 1 << 20

// requestBodyReadTimeout bounds how long reading a single HTTP request body may take (a slowloris
// guard). The server sets only ReadHeaderTimeout — a global http.Server.ReadTimeout would kill the
// long-lived /ws signaling stream — so the body deadline is applied per-request here, after the
// /ws upgrade is exempted. 30s is generous for the app's small JSON/form bodies (KB, far under the
// 1 MiB cap) yet bounds a stalled chunked/slow body so it can't pin a handler goroutine.
const requestBodyReadTimeout = 30 * time.Second

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
// To stop a slowloris client from stalling that read (or a handler's own read) and pinning a
// goroutine, it sets a per-request body read deadline (requestBodyReadTimeout) before reading.
//
// Only a genuine WebSocket upgrade to a signaling route is exempt — that body is the hijacked streaming socket,
// not a finite request body, and capping (or deadlining) it would corrupt the stream. The
// exemption is gated on the upgrade handshake (not the path alone), so a stray POST /ws or a
// non-upgrade GET /ws is still capped like any other route.
func RequestBodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSignalingWSPath(r.URL.Path) && isWebSocketUpgrade(r) { // streaming signaling — exempt (D-M5.5-4)
				next.ServeHTTP(w, r)
				return
			}
			// Bound the body read (slowloris guard): a stalled chunked/slow body can't pin a handler
			// goroutine. Best-effort — SetReadDeadline is unsupported on some ResponseWriters (e.g.
			// httptest's recorder), where it errors and is harmlessly skipped. /ws is exempted above,
			// so a real signaling stream is never deadlined.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(requestBodyReadTimeout))

			// Declared length over the cap: reject without reading the body.
			if r.ContentLength > maxBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			if r.ContentLength < 0 {
				// Unknown length: buffer up to the cap so an oversized chunked body is rejected here,
				// not silently accepted by a handler that never reads it. Reading cap+1 bytes proves
				// "over the cap" without buffering the whole (potentially unbounded) stream; the read
				// deadline above bounds how long a stalled stream can hold this goroutine.
				body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
				_ = r.Body.Close()
				if err != nil {
					if errors.Is(err, os.ErrDeadlineExceeded) {
						http.Error(w, "request body read timed out", http.StatusRequestTimeout)
						return
					}
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

func isSignalingWSPath(path string) bool {
	if path == "/ws" {
		return true
	}
	parts := strings.Split(path, "/")
	return len(parts) == 4 && parts[0] == "" && parts[1] == "s" && parts[2] != "" && parts[3] == "ws"
}

// isWebSocketUpgrade reports whether r is a genuine WebSocket upgrade handshake (RFC 6455): a GET
// carrying `Connection: Upgrade` (a comma-listed, case-insensitive token), `Upgrade: websocket`,
// `Sec-WebSocket-Version: 13`, and a well-formed `Sec-WebSocket-Key` (base64 of 16 bytes, per the
// RFC — the form every compliant client sends). Requiring the full, validated handshake — not just
// the Connection/Upgrade pair or any non-empty key — means a malformed request that merely sets
// these headers (and would be rejected by websocket.Accept anyway) is NOT exempted, so it still
// goes through the body cap rather than skipping it.
func isWebSocketUpgrade(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" || !validWebSocketKey(r.Header.Get("Sec-WebSocket-Key")) {
		return false
	}
	// "Connection" and "Upgrade" may each arrive comma-joined OR split across multiple header fields
	// (a proxy can do either, and they're equivalent per RFC 7230), and the token may sit in a list.
	// Header.Get reads only the first field and EqualFold demands an exact match, so scan every value
	// for the token — matching what websocket.Accept itself accepts, so a genuine upgrade behind such
	// a proxy is still exempted rather than wrongly capped.
	return headerListContainsToken(r.Header.Values("Connection"), "upgrade") &&
		headerListContainsToken(r.Header.Values("Upgrade"), "websocket")
}

// headerListContainsToken reports whether token (case-insensitive) appears among the comma-listed
// tokens across all of values — the multi-field, comma-joined form HTTP allows for list headers.
func headerListContainsToken(values []string, token string) bool {
	for _, v := range values {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), token) {
				return true
			}
		}
	}
	return false
}

// validWebSocketKey reports whether k is a well-formed Sec-WebSocket-Key: the base64 encoding of a
// 16-byte value (RFC 6455 §11.3.1). Every compliant client sends this form; rejecting anything else
// keeps the body-cap exemption to requests that are actually shaped like a real handshake.
func validWebSocketKey(k string) bool {
	b, err := base64.StdEncoding.DecodeString(k)
	return err == nil && len(b) == 16
}
