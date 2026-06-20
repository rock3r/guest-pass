package web

import "net/http"

// defaultMaxRequestBodyBytes is the fallback global request-body cap (1 MiB) used when the
// router is wired without an explicit limit. The production limit is config-backed
// (MAX_REQUEST_BODY_BYTES, D-M5.5-4) and validated positive at load.
const defaultMaxRequestBodyBytes = 1 << 20

// RequestBodyLimit caps the request body on every non-streaming route (D-M5.5-4 / AC-8). A
// request whose declared Content-Length exceeds the cap is rejected outright with 413 and the
// wrapped handler never runs; for a chunked/unknown-length body the cap is still enforced via
// http.MaxBytesReader, so an unbounded body can never be buffered. /ws is exempt: it is the
// streaming signaling endpoint (the body is the hijacked WebSocket, not a finite request body),
// so a cap there would corrupt the stream.
func RequestBodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ws" { // streaming signaling — exempt (D-M5.5-4)
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
