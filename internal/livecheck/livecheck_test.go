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

// The response-size cap (§7.4) bounds the read AND an oversized body degrades to unavailable rather
// than a false "offline" — a truncated page whose live marker is past the cap can't be trusted.
func TestTwitchVerify_OversizeDegradesToUnavailable(t *testing.T) {
	signal := `"isLiveBroadcast":true`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1024))) // 1 KiB of filler...
		_, _ = w.Write([]byte(signal))                    // ...then the live signal, beyond a tiny cap
	}))
	defer srv.Close()

	v := newTwitchVerifier(srv.Client(), srv.URL)
	v.maxBody = 64 // body (1 KiB+) exceeds the cap → oversized → unavailable, NOT a false offline
	if got := v.verify(context.Background(), "chan"); got != StatusUnavailable {
		t.Fatalf("oversized body status = %q, want status-unavailable (truncated, can't trust)", got)
	}

	// With a generous cap the same body fits and the signal is read → live (control case).
	v.maxBody = maxBodyBytes
	if got := v.verify(context.Background(), "chan"); got != StatusLive {
		t.Fatalf("within the cap the signal should be read, status = %q, want live", got)
	}
}

func TestYouTubeNormalize(t *testing.T) {
	v := newYouTubeVerifier(http.DefaultClient, youtubeBaseURL)
	ok := map[string]string{
		"@MrBeast": "mrbeast", "MrBeast": "mrbeast", "a_b.c-1": "a_b.c-1", "  @Foo  ": "foo", "abc": "abc",
	}
	for in, want := range ok {
		if got, valid := v.normalize(in); !valid || got != want {
			t.Errorf("normalize(%q) = (%q,%v), want (%q,true)", in, got, valid, want)
		}
	}
	bad := []string{
		"", "@", "ab", strings.Repeat("x", 31), "has space", "slash/x", "https://youtube.com/@x",
		".lead", "trail.", "-lead", "trail-", "dot..dot", "uni€",
	}
	for _, in := range bad {
		if got, valid := v.normalize(in); valid {
			t.Errorf("normalize(%q) = (%q,true), want invalid", in, got)
		}
	}
}

func TestYouTubeWatchURL(t *testing.T) {
	v := newYouTubeVerifier(http.DefaultClient, youtubeBaseURL)
	if got := v.watchURL("@MrBeast"); got != "https://www.youtube.com/@mrbeast" {
		t.Errorf("watchURL = %q", got)
	}
	if got := v.watchURL("bad handle"); got != "" {
		t.Errorf("watchURL(invalid) = %q, want empty", got)
	}
}

// verify fetches /@handle/live, follows YouTube's (on-domain) redirect to the live watch page, and
// reads the isLiveNow flag — true only while broadcasting, so an ended stream (isLiveContent:true,
// isLiveNow:false) reads offline. Any non-200/transport failure degrades to unavailable (§7.4).
func TestYouTubeVerify_ParsesAndDegrades(t *testing.T) {
	var path string
	mux := http.NewServeMux()
	mux.HandleFunc("/@liveone/live", func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`<html><script>var ytInitialPlayerResponse = {"videoDetails":{"isLiveContent":true},"microformat":{"playerMicroformatRenderer":{"liveBroadcastDetails":{"isLiveNow":true}}}};</script></html>`))
	})
	mux.HandleFunc("/@offchan/live", func(w http.ResponseWriter, _ *http.Request) {
		// The MAIN player response is NOT live (isLiveNow:false), and a RECOMMENDED stream in the
		// separate ytInitialData script IS live — must read offline: the recommendation's flag must
		// not leak into this channel's status (codex: scope the parse to the main player response).
		_, _ = w.Write([]byte(`<html><script>var ytInitialPlayerResponse = {"videoDetails":{"isLiveContent":true},"microformat":{"playerMicroformatRenderer":{"liveBroadcastDetails":{"isLiveNow":false}}}};</script><script>var ytInitialData = {"recommended":[{"title":"someone else live","liveBroadcastDetails":{"isLiveNow":true}}]};</script></html>`))
	})
	mux.HandleFunc("/@noplayer/live", func(w http.ResponseWriter, _ *http.Request) {
		// An offline channel's /live page with no player response at all → offline (degrade-safe).
		_, _ = w.Write([]byte(`<html><body>This channel isn't live right now.</body></html>`))
	})
	mux.HandleFunc("/@missing/live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/@boom/live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	v := newYouTubeVerifier(srv.Client(), srv.URL)
	ctx := context.Background()

	if got := v.verify(ctx, "@liveone"); got != StatusLive {
		t.Errorf("live channel = %q, want live", got)
	}
	if path != "/@liveone/live" {
		t.Errorf("fetched path = %q, want /@handle/live", path)
	}
	if got := v.verify(ctx, "offchan"); got != StatusOffline {
		t.Errorf("offline channel w/ a live RECOMMENDATION = %q, want offline (scoped to player response)", got)
	}
	if got := v.verify(ctx, "noplayer"); got != StatusOffline {
		t.Errorf("no player response = %q, want offline", got)
	}
	if got := v.verify(ctx, "missing"); got != StatusUnavailable {
		t.Errorf("404 = %q, want status-unavailable", got)
	}
	if got := v.verify(ctx, "boom"); got != StatusUnavailable {
		t.Errorf("5xx = %q, want status-unavailable", got)
	}
	if got := v.verify(ctx, "bad handle"); got != StatusUnavailable {
		t.Errorf("invalid handle = %q, want status-unavailable (no fetch)", got)
	}

	srv.Close()
	if got := v.verify(ctx, "liveone"); got != StatusUnavailable {
		t.Errorf("dead server = %q, want status-unavailable", got)
	}
}

// The §7.4 size cap bounds the read AND an oversized body degrades to unavailable rather than a false
// "offline" — a live marker past the truncation point can't be trusted (mirrors Twitch).
func TestYouTubeVerify_OversizeDegradesToUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1024))) // 1 KiB of filler...
		_, _ = w.Write([]byte(`<script>var ytInitialPlayerResponse = {"liveBroadcastDetails":{"isLiveNow":true}};</script>`))
	}))
	defer srv.Close()
	v := newYouTubeVerifier(srv.Client(), srv.URL)
	v.maxBody = 64
	if got := v.verify(context.Background(), "somechan"); got != StatusUnavailable {
		t.Fatalf("oversized body = %q, want status-unavailable", got)
	}
	v.maxBody = maxBodyBytes
	if got := v.verify(context.Background(), "somechan"); got != StatusLive {
		t.Fatalf("within the cap the signal should be read, status = %q, want live", got)
	}
}

// T-6 (YouTube SSRF): the YouTube fetch path goes through the SSRF-closed client, so even a target
// that resolves to loopback (the httptest server) is refused at dial — the verify degrades rather
// than scraping it, despite the page parsing as live.
func TestYouTubeVerify_SSRFBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`"isLiveNow":true`)) // would read live if the dial weren't blocked
	}))
	defer srv.Close()
	v := newYouTubeVerifier(newSafeClient(), srv.URL) // the REAL SSRF-closed client, not srv.Client()
	if got := v.verify(context.Background(), "somechan"); got != StatusUnavailable {
		t.Fatalf("YouTube verify against a loopback target = %q, want status-unavailable (SSRF-blocked)", got)
	}
}

// A result produced under a caller-cancelled context must NOT be cached — otherwise one caller's
// cancellation poisons the shared (platform,channel) entry, denying other clients a retry for the
// whole TTL (codex/bugbot).
func TestChecker_DoesNotCacheCancelledContext(t *testing.T) {
	fake := &fakeVerifier{status: StatusLive}
	c := &Checker{
		verifiers: map[string]verifier{PlatformTwitch: fake},
		cache:     map[string]cacheEntry{},
		ttl:       45 * time.Second,
		now:       time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled when Check runs

	c.Check(ctx, PlatformTwitch, "ninja")
	if got := atomic.LoadInt32(&fake.calls); got != 1 {
		t.Fatalf("first (cancelled) check verify calls = %d, want 1", got)
	}
	// A subsequent check with a live context must re-fetch (the cancelled result wasn't cached).
	c.Check(context.Background(), PlatformTwitch, "ninja")
	if got := atomic.LoadInt32(&fake.calls); got != 2 {
		t.Fatalf("after a cancelled check, verify calls = %d, want 2 (not cached → re-fetched)", got)
	}
}

func TestParseTwitchLive(t *testing.T) {
	// Live across arbitrary JSON whitespace around the colon (codex): no space, one space, spaces,
	// a newline/tab after the colon.
	for _, live := range []string{
		`...,"isLiveBroadcast":true,...`,
		`...,"isLiveBroadcast": true,...`,
		`..."isLiveBroadcast"  :  true...`,
		"...\"isLiveBroadcast\":\n  true...",
		"...\"isLiveBroadcast\" :\ttrue...",
	} {
		if parseTwitchLive([]byte(live)) != StatusLive {
			t.Errorf("should be live: %q", live)
		}
	}
	for _, off := range []string{
		`...,"isLiveBroadcast":false,...`,
		`no signal here`,
		`"isLiveBroadcast":truething`, // word-boundary: not the boolean true
	} {
		if parseTwitchLive([]byte(off)) != StatusOffline {
			t.Errorf("should be offline: %q", off)
		}
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
