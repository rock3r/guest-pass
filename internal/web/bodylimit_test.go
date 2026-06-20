package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readEcho is a next handler that drains the body and reports whether the read succeeded,
// so the cap-enforcement (read error past the limit) is observable in the chunked case.
func readEcho(t *testing.T) (http.HandlerFunc, *bool, *error) {
	t.Helper()
	called := false
	var readErr error
	h := func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, readErr = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}
	return h, &called, &readErr
}

// A declared body over the cap is rejected with 413 by the middleware; the wrapped handler
// never runs (AC-8 / D-M5.5-4).
func TestRequestBodyLimit_OversizeContentLengthRejected(t *testing.T) {
	next, called, _ := readEcho(t)
	mw := RequestBodyLimit(16)(next)

	req := httptest.NewRequest(http.MethodPost, "/api/streams", strings.NewReader(strings.Repeat("x", 64)))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body = %d, want 413", rec.Code)
	}
	if *called {
		t.Fatal("the wrapped handler must not run for an oversize body")
	}
}

// A body within the cap passes through to the handler unchanged.
func TestRequestBodyLimit_WithinCapPasses(t *testing.T) {
	next, called, readErr := readEcho(t)
	mw := RequestBodyLimit(1024)(next)

	req := httptest.NewRequest(http.MethodPost, "/api/streams", strings.NewReader(`{"title":"ok"}`))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("within-cap body = %d, want 200", rec.Code)
	}
	if !*called {
		t.Fatal("the wrapped handler should run for a within-cap body")
	}
	if *readErr != nil {
		t.Fatalf("within-cap body read errored: %v", *readErr)
	}
}

// A genuine WebSocket upgrade to /ws is exempt (the streaming signaling endpoint, D-M5.5-4): the
// cap must not truncate the hijacked stream, so even an oversize declared body passes through.
func TestRequestBodyLimit_WSUpgradeExempt(t *testing.T) {
	next, called, _ := readEcho(t)
	mw := RequestBodyLimit(16)(next)

	req := httptest.NewRequest(http.MethodGet, "/ws", strings.NewReader(strings.Repeat("x", 64)))
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !*called {
		t.Fatal("a genuine /ws upgrade must be exempt from the body cap")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("/ws upgrade exempt = %d, want 200", rec.Code)
	}
}

// A non-upgrade request to /ws (e.g. POST /ws, or a GET without the upgrade handshake) is NOT
// exempt — the exemption is gated on the upgrade headers, not the path, so the cap still applies.
func TestRequestBodyLimit_WSNonUpgradeCapped(t *testing.T) {
	next, called, _ := readEcho(t)
	mw := RequestBodyLimit(16)(next)

	req := httptest.NewRequest(http.MethodPost, "/ws", strings.NewReader(strings.Repeat("x", 64)))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("non-upgrade POST /ws = %d, want 413", rec.Code)
	}
	if *called {
		t.Fatal("a non-upgrade /ws request must not bypass the cap")
	}
}

// Even without a declared Content-Length (chunked), the cap is enforced: the handler's read
// past the limit fails, so an unbounded body can't be buffered.
func TestRequestBodyLimit_ChunkedEnforced(t *testing.T) {
	next, called, readErr := readEcho(t)
	mw := RequestBodyLimit(16)(next)

	req := httptest.NewRequest(http.MethodPost, "/api/streams", strings.NewReader(strings.Repeat("x", 64)))
	req.ContentLength = -1 // unknown length (chunked)
	req.Header.Del("Content-Length")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !*called {
		t.Fatal("a chunked body reaches the handler (no Content-Length fast path)")
	}
	if *readErr == nil {
		t.Fatal("reading a chunked body past the cap must error (cap enforced via MaxBytesReader)")
	}
}
