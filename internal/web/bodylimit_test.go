package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// deadlineBody is a request body whose Read always reports a read-deadline timeout, simulating a
// slowloris stream that the per-request body deadline tripped.
type deadlineBody struct{}

func (deadlineBody) Read([]byte) (int, error) { return 0, os.ErrDeadlineExceeded }
func (deadlineBody) Close() error             { return nil }

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

// wsUpgradeReq builds a GET /ws carrying a full RFC 6455 handshake plus an over-cap body.
func wsUpgradeReq() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/ws", strings.NewReader(strings.Repeat("x", 64)))
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")
	return req
}

// A genuine WebSocket upgrade to /ws is exempt (the streaming signaling endpoint, D-M5.5-4): the
// cap must not truncate the hijacked stream, so even an oversize declared body passes through.
func TestRequestBodyLimit_WSUpgradeExempt(t *testing.T) {
	next, called, _ := readEcho(t)
	mw := RequestBodyLimit(16)(next)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, wsUpgradeReq())

	if !*called {
		t.Fatal("a genuine /ws upgrade must be exempt from the body cap")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("/ws upgrade exempt = %d, want 200", rec.Code)
	}
}

// An OBS source uses its own source-scoped signaling endpoint so a Cloudflare Access bypass can
// remain narrow. It is still a genuine WebSocket stream and must receive the same body-cap
// exemption as the host/guest endpoint.
func TestRequestBodyLimit_SourceWSUpgradeExempt(t *testing.T) {
	next, called, _ := readEcho(t)
	mw := RequestBodyLimit(16)(next)

	req := wsUpgradeReq()
	req.URL.Path = "/s/cam-1/ws"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !*called {
		t.Fatal("a genuine source WebSocket upgrade must be exempt from the body cap")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("source WebSocket upgrade exempt = %d, want 200", rec.Code)
	}
}

// A genuine upgrade whose Connection/Upgrade tokens are SPLIT across multiple header fields or sit
// in a comma list (as a proxy may send them) is still recognized and exempt — Header.Get + an exact
// match would miss the token, but websocket.Accept scans all values, and so must the exemption.
func TestRequestBodyLimit_WSUpgradeLenientHeaders(t *testing.T) {
	mw := RequestBodyLimit(16)

	t.Run("split Connection field", func(t *testing.T) {
		next, called, _ := readEcho(t)
		req := wsUpgradeReq()
		req.Header.Del("Connection")
		req.Header.Add("Connection", "keep-alive") // first field lacks the Upgrade token
		req.Header.Add("Connection", "Upgrade")    // ...it's in a second Connection field
		rec := httptest.NewRecorder()
		mw(next).ServeHTTP(rec, req)
		if !*called || rec.Code != http.StatusOK {
			t.Fatalf("split-Connection upgrade: called=%v code=%d, want true/200", called, rec.Code)
		}
	})

	t.Run("listed Upgrade token", func(t *testing.T) {
		next, called, _ := readEcho(t)
		req := wsUpgradeReq()
		req.Header.Set("Upgrade", "websocket, foo") // token in a comma list, not an exact match
		rec := httptest.NewRecorder()
		mw(next).ServeHTTP(rec, req)
		if !*called || rec.Code != http.StatusOK {
			t.Fatalf("listed-Upgrade upgrade: called=%v code=%d, want true/200", called, rec.Code)
		}
	})
}

// A non-upgrade request to /ws (POST /ws, or a GET missing the full handshake — e.g. only the
// Connection/Upgrade pair without Sec-WebSocket-Key/Version) is NOT exempt: the exemption requires
// the genuine handshake, not the path or a partial header set, so the cap still applies.
func TestRequestBodyLimit_WSNonUpgradeCapped(t *testing.T) {
	cases := map[string]func() *http.Request{
		"POST /ws": func() *http.Request {
			return httptest.NewRequest(http.MethodPost, "/ws", strings.NewReader(strings.Repeat("x", 64)))
		},
		"partial handshake (no Sec-WebSocket-Key/Version)": func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/ws", strings.NewReader(strings.Repeat("x", 64)))
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			return req
		},
		"malformed Sec-WebSocket-Key (not 16-byte base64)": func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/ws", strings.NewReader(strings.Repeat("x", 64)))
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Sec-WebSocket-Key", "not-a-real-key")
			req.Header.Set("Sec-WebSocket-Version", "13")
			return req
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			next, called, _ := readEcho(t)
			mw := RequestBodyLimit(16)(next)
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, mk())
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s = %d, want 413", name, rec.Code)
			}
			if *called {
				t.Fatalf("%s must not bypass the cap", name)
			}
		})
	}
}

// An oversized chunked/unknown-length body is rejected with 413 BEFORE the handler runs, so a
// handler that never reads the body still can't execute on an oversized request.
func TestRequestBodyLimit_ChunkedOversizeRejected(t *testing.T) {
	next, called, _ := readEcho(t)
	mw := RequestBodyLimit(16)(next)

	req := httptest.NewRequest(http.MethodPost, "/api/streams", strings.NewReader(strings.Repeat("x", 64)))
	req.ContentLength = -1 // unknown length (chunked)
	req.Header.Del("Content-Length")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize chunked body = %d, want 413", rec.Code)
	}
	if *called {
		t.Fatal("the wrapped handler must not run for an oversize chunked body")
	}
}

// A chunked body whose read times out (the slowloris guard's deadline firing) is rejected with
// 408 before dispatch, so a stalled stream can't pin the handler goroutine.
func TestRequestBodyLimit_ChunkedReadTimeout(t *testing.T) {
	next, called, _ := readEcho(t)
	mw := RequestBodyLimit(1024)(next)

	req := httptest.NewRequest(http.MethodPost, "/api/streams", nil)
	req.Body = deadlineBody{}
	req.ContentLength = -1 // unknown length (chunked)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("stalled chunked read = %d, want 408", rec.Code)
	}
	if *called {
		t.Fatal("the handler must not run when the body read times out")
	}
}

// A within-cap chunked body passes through to the handler intact (the middleware buffers and
// re-provides it), so legitimate unknown-length requests still work.
func TestRequestBodyLimit_ChunkedWithinCapPasses(t *testing.T) {
	called := false
	var got string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusOK)
	})
	mw := RequestBodyLimit(64)(next)

	req := httptest.NewRequest(http.MethodPost, "/api/streams", strings.NewReader("hello chunked"))
	req.ContentLength = -1 // unknown length (chunked)
	req.Header.Del("Content-Length")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("within-cap chunked: called=%v code=%d, want true/200", called, rec.Code)
	}
	if got != "hello chunked" {
		t.Fatalf("handler saw body %q, want it re-provided intact", got)
	}
}
