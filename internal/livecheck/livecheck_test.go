package livecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTwitchNormalize(t *testing.T) {
	v := newTwitchVerifier(http.DefaultClient, twitchBaseURL)
	ok := map[string]string{"Ninja": "ninja", "abc": "abc", "a_b_1": "a_b_1", "  Foo  ": "foo"}
	for in, want := range ok {
		if got, valid := v.normalize(in); !valid || got != want {
			t.Errorf("normalize(%q) = (%q,%v), want (%q,true)", in, got, valid, want)
		}
	}
	bad := []string{"", "ab", "this_name_is_way_too_long_for_twitch", "has space", "bad-dash", "dot.name", "slash/x", "@handle", "https://twitch.tv/x", "unié"}
	for _, in := range bad {
		if got, valid := v.normalize(in); valid {
			t.Errorf("normalize(%q) = (%q,true), want invalid", in, got)
		}
	}
}

func TestTwitchWatchURL(t *testing.T) {
	v := newTwitchVerifier(http.DefaultClient, twitchBaseURL)
	if got := v.watchURL("Ninja"); got != "https://www.twitch.tv/ninja" {
		t.Errorf("watchURL = %q", got)
	}
	if got := v.watchURL("bad dash"); got != "" {
		t.Errorf("watchURL(invalid) = %q, want empty", got)
	}
}

// verify parses the live signal from a (test) channel page and degrades to unavailable on any
// non-200 / transport failure — the §7.4 best-effort contract.
func TestTwitchVerify_ParsesAndDegrades(t *testing.T) {
	var path string
	mux := http.NewServeMux()
	mux.HandleFunc("/live", func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`<html><script type="application/ld+json">{"@type":"VideoObject","isLiveBroadcast":true}</script></html>`))
	})
	mux.HandleFunc("/off", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>some channel, not live</body></html>`))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/boom", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Inject the httptest client + base (bypasses the SSRF guard, which is tested separately —
	// httptest is loopback). This exercises the fetch+parse path.
	v := newTwitchVerifier(srv.Client(), srv.URL)
	ctx := context.Background()

	if got := v.verify(ctx, "live"); got != StatusLive {
		t.Errorf("live channel = %q, want live", got)
	}
	if path != "/live" {
		t.Errorf("fetched path = %q, want the channel appended to the base", path)
	}
	if got := v.verify(ctx, "off"); got != StatusOffline {
		t.Errorf("offline channel = %q, want offline", got)
	}
	if got := v.verify(ctx, "missing"); got != StatusUnavailable {
		t.Errorf("404 channel = %q, want status-unavailable", got)
	}
	if got := v.verify(ctx, "boom"); got != StatusUnavailable {
		t.Errorf("5xx channel = %q, want status-unavailable", got)
	}
	if got := v.verify(ctx, "bad dash"); got != StatusUnavailable {
		t.Errorf("invalid channel = %q, want status-unavailable (no fetch)", got)
	}

	// A dead server (no listener) degrades, never panics.
	srv.Close()
	if got := v.verify(ctx, "live"); got != StatusUnavailable {
		t.Errorf("dead server = %q, want status-unavailable", got)
	}
}

// The response-size cap (§7.4) truncates the read: a live signal placed BEYOND the cap is not
// seen, so an oversize page can't be read unbounded. Proven by lowering the cap below the signal.
func TestTwitchVerify_SizeCapTruncates(t *testing.T) {
	signal := `"isLiveBroadcast":true`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1024))) // 1 KiB of filler...
		_, _ = w.Write([]byte(signal))                    // ...then the live signal, beyond a tiny cap
	}))
	defer srv.Close()

	v := newTwitchVerifier(srv.Client(), srv.URL)
	v.maxBody = 64 // cap below the 1 KiB filler → the signal past the cap is never read
	if got := v.verify(context.Background(), "chan"); got != StatusOffline {
		t.Fatalf("with the signal beyond the size cap, status = %q, want offline (cap truncated the read)", got)
	}

	// With a generous cap the same body reads the signal → live (control case).
	v.maxBody = maxBodyBytes
	if got := v.verify(context.Background(), "chan"); got != StatusLive {
		t.Fatalf("within the cap the signal should be read, status = %q, want live", got)
	}
}

func TestParseTwitchLive(t *testing.T) {
	if parseTwitchLive([]byte(`...,"isLiveBroadcast":true,...`)) != StatusLive {
		t.Error("isLiveBroadcast:true should be live")
	}
	if parseTwitchLive([]byte(`...,"isLiveBroadcast": true,...`)) != StatusLive {
		t.Error("isLiveBroadcast: true (spaced) should be live")
	}
	if parseTwitchLive([]byte(`...,"isLiveBroadcast":false,...`)) != StatusOffline {
		t.Error("isLiveBroadcast:false should be offline")
	}
	if parseTwitchLive([]byte(`no signal here`)) != StatusOffline {
		t.Error("no signal should be offline")
	}
}

// fakeVerifier counts verify calls so cache behavior can be asserted without a network.
type fakeVerifier struct {
	calls  int32
	status Status
}

func (f *fakeVerifier) verify(context.Context, string) Status {
	atomic.AddInt32(&f.calls, 1)
	return f.status
}
func (f *fakeVerifier) watchURL(ch string) string          { return "https://example.test/" + ch }
func (f *fakeVerifier) normalize(ch string) (string, bool) { return ch, ch != "" }

// Check caches within the TTL (one fetch per channel per window), re-fetches after expiry, and
// returns unavailable+no-watch for an unknown platform or invalid channel without any fetch.
func TestChecker_CacheAndDegrade(t *testing.T) {
	fake := &fakeVerifier{status: StatusLive}
	now := time.Unix(1_000_000, 0)
	c := &Checker{
		verifiers: map[string]verifier{PlatformTwitch: fake},
		cache:     map[string]cacheEntry{},
		ttl:       45 * time.Second,
		now:       func() time.Time { return now },
	}
	ctx := context.Background()

	r := c.Check(ctx, PlatformTwitch, "ninja")
	if r.Status != StatusLive || r.WatchURL == "" {
		t.Fatalf("first check = %+v, want live + watch url", r)
	}
	c.Check(ctx, PlatformTwitch, "ninja") // within TTL → cached
	c.Check(ctx, PlatformTwitch, "ninja")
	if got := atomic.LoadInt32(&fake.calls); got != 1 {
		t.Fatalf("verify called %d times within TTL, want 1 (cached)", got)
	}

	now = now.Add(46 * time.Second) // past TTL → re-fetch
	c.Check(ctx, PlatformTwitch, "ninja")
	if got := atomic.LoadInt32(&fake.calls); got != 2 {
		t.Fatalf("verify called %d times after TTL, want 2 (re-fetched)", got)
	}

	// Unknown platform → unavailable, no fetch, no watch URL.
	if r := c.Check(ctx, "youtube", "x"); r.Status != StatusUnavailable || r.WatchURL != "" {
		t.Fatalf("unknown platform = %+v, want unavailable + no watch", r)
	}
	// Invalid channel → unavailable, no fetch.
	before := atomic.LoadInt32(&fake.calls)
	if r := c.Check(ctx, PlatformTwitch, ""); r.Status != StatusUnavailable || r.WatchURL != "" {
		t.Fatalf("invalid channel = %+v, want unavailable + no watch", r)
	}
	if atomic.LoadInt32(&fake.calls) != before {
		t.Fatal("invalid channel must not trigger a fetch")
	}
}
