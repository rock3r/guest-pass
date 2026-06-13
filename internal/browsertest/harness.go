//go:build browser

// Package browsertest is the chromedp browser-test layer (AD-9): pure-Go headless Chrome,
// no npm / Playwright / Jest. It builds the real frontend bundles (internal/assets), serves
// them, and drives them in headless Chrome with FAKE MEDIA — a synthetic camera/mic and an
// auto-accepted getUserMedia prompt — so the islands and OBS source pages can be exercised
// end to end with no hardware. It is compiled only under the `browser` build tag and run
// via `go test -tags browser ./internal/browsertest/...` (locally and in the CI Chrome job).
//
// What this layer proves is PLUMBING, not capacity, real networks, or real OBS-CEF (RF-7):
// those remain a manual owner DoD.
package browsertest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/rock3r/guest-pass/internal/assets"
)

// repoRoot returns the repo root from a test's working directory (the package dir,
// internal/browsertest, when `go test` runs) so the esbuild build can resolve web/src.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	return root
}

// BuildDist builds the frontend bundles into a fresh temp dir and returns its path, so a
// test can serve them (e.g. as the real router's static dir, which also reads the SRI
// manifest from here). The dir is cleaned up with the test.
func BuildDist(t *testing.T) string {
	t.Helper()
	dist := t.TempDir()
	if err := assets.Build(repoRoot(t), dist); err != nil {
		t.Fatalf("assets.Build: %v", err)
	}
	return dist
}

// Serve starts a test server for handler and returns it (closed with the test). Use with
// BuildDist when a test needs the real HTTP handler (e.g. web.NewRouter) rather than the
// bare static mux that New provides.
func Serve(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// Harness is a running test server fronting the freshly-built frontend bundles.
type Harness struct {
	URL  string // base URL of the test server
	Dist string // temp dir holding the built bundles, served at /static
}

// New builds the frontend into a temp dir and starts a test server that serves the bundles
// at /static plus any page routes the caller registers.
func New(t *testing.T, routes func(*http.ServeMux)) *Harness {
	t.Helper()
	dist := BuildDist(t)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(dist))))
	if routes != nil {
		routes(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Harness{URL: srv.URL, Dist: dist}
}

// Page returns a handler that serves html as an HTML document.
func Page(html string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, html)
	}
}

// fakeMediaAllocOpts builds the chromedp ExecAllocator options for a headless Chrome with
// fake media: the `use-fake-device-for-media-stream` / `use-fake-ui-for-media-stream` flags
// supply a synthetic cam/mic and auto-accept the permission prompt (no hardware).
// `--no-sandbox` keeps it runnable as root in the CI container; CHROME_PATH overrides the
// binary when set (the CI job points it at the installed Chrome). Reused by multi-tab tests.
func fakeMediaAllocOpts() []chromedp.ExecAllocatorOption {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.NoSandbox,
		chromedp.Flag("use-fake-device-for-media-stream", true),
		chromedp.Flag("use-fake-ui-for-media-stream", true),
	)
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	return opts
}

// Chrome runs fn in a single-tab chromedp context backed by a headless Chrome with fake
// media (see fakeMediaAllocOpts).
func Chrome(t *testing.T, timeout time.Duration, fn func(ctx context.Context)) {
	t.Helper()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), fakeMediaAllocOpts()...)
	defer cancelAlloc()
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	defer cancelCtx()
	ctx, cancelTimeout := context.WithTimeout(ctx, timeout)
	defer cancelTimeout()
	fn(ctx)
}
