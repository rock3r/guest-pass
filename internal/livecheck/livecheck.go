// Package livecheck implements the optional, zero-config, SSRF-closed live-status verification of a
// host's linked streaming channel (D-29 / §7.4 / AC-7). It is best-effort: any fetch/parse/rate-limit
// failure degrades to StatusUnavailable (the D-24 "on-air status unavailable" signal) and never an
// error or panic. v1 supports Twitch only (DEF-4); the verifier registry keeps YouTube a small add
// (M5.5). All outbound fetches go through the SSRF-closed client (ssrf.go): a fixed platform domain,
// a validating dialer that blocks private/loopback/link-local/metadata IPs on the actual dialed IP,
// on-domain http(s)-only redirects, and tight timeout/size caps.
package livecheck

import (
	"context"
	"sync"
	"time"
)

// Status is the live-verify outcome folded into D-24's broadcast layer.
type Status string

const (
	StatusLive        Status = "live"               // the channel is broadcasting now
	StatusOffline     Status = "offline"            // the channel is not broadcasting
	StatusUnavailable Status = "status-unavailable" // best-effort failure → D-24 degrade
)

// Platform identifiers. v1 = Twitch only (DEF-4); YouTube lands in M5.5.
const (
	PlatformTwitch  = "twitch"
	PlatformYouTube = "youtube"
)

// pollTTL is how long a channel's result is cached before a fresh fetch — the "politely polled
// (~30–60s), shared across clients" cadence (D-29): repeated checks within the window are served
// from cache, so the platform isn't hammered.
const pollTTL = 45 * time.Second

// Result is a verification outcome: the live Status plus the public "watch live" link for the guest
// (D-29), set whenever the channel id is valid even if the live check itself degrades.
type Result struct {
	Status    Status
	WatchURL  string
	CheckedAt int64 // unix seconds the result was produced (0 for a never-fetched invalid channel)
}

// verifier checks one platform. Implementations are best-effort: verify returns StatusUnavailable
// (never an error) on any failure so the caller degrades cleanly (D-24).
type verifier interface {
	verify(ctx context.Context, channel string) Status
	watchURL(channel string) string          // public watch link, or "" if the channel id is invalid
	normalize(channel string) (string, bool) // sanitize/validate; ("",false) if invalid
}

// Checker is the live-verify entry point: a platform registry + a per-(platform,channel) TTL cache.
type Checker struct {
	mu        sync.Mutex
	verifiers map[string]verifier
	cache     map[string]cacheEntry
	ttl       time.Duration
	now       func() time.Time // injectable clock for tests
}

type cacheEntry struct {
	res     Result
	expires time.Time
}

// NewChecker builds the production checker: the SSRF-closed HTTP client wired to the Twitch verifier
// against the fixed twitch.tv template, with the polite poll TTL.
func NewChecker() *Checker {
	client := newSafeClient()
	return &Checker{
		verifiers: map[string]verifier{
			PlatformTwitch: newTwitchVerifier(client, twitchBaseURL),
		},
		cache: map[string]cacheEntry{},
		ttl:   pollTTL,
		now:   time.Now,
	}
}

// Check returns the live status + watch link for (platform, channel). It serves a cached result
// within the TTL and otherwise fetches fresh. An unknown platform or an invalid channel id returns
// StatusUnavailable with no watch link and performs NO network fetch. It never returns an error —
// best-effort, degrading to StatusUnavailable (D-24).
func (c *Checker) Check(ctx context.Context, platform, channel string) Result {
	v, ok := c.verifiers[platform]
	if !ok {
		return Result{Status: StatusUnavailable}
	}
	norm, ok := v.normalize(channel)
	if !ok {
		return Result{Status: StatusUnavailable}
	}
	key := platform + "|" + norm

	c.mu.Lock()
	if e, ok := c.cache[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.res
	}
	c.mu.Unlock()

	// Fetch OUTSIDE the lock (network I/O must not serialize all checks). A rare duplicate cold
	// fetch for the same key is acceptable for the single-instance v1; the TTL keeps it polite.
	status := v.verify(ctx, norm)
	res := Result{Status: status, WatchURL: v.watchURL(norm), CheckedAt: c.now().Unix()}

	// Do NOT cache a result produced under a caller-cancelled/expired context: that
	// status-unavailable reflects the caller, not the platform, and caching it would poison the
	// shared (platform,channel) entry for up to the TTL, denying other clients a retry (codex/bugbot).
	if ctx.Err() == nil {
		c.mu.Lock()
		c.cache[key] = cacheEntry{res: res, expires: c.now().Add(c.ttl)}
		c.mu.Unlock()
	}
	return res
}
