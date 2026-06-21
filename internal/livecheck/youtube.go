package livecheck

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const (
	// youtubeBaseURL is the FIXED template host (§7.4): the channel handle is only ever appended as a
	// sanitized path segment (/@handle…), never the host — so a host-supplied value can't redirect the
	// fetch to an arbitrary address. The SSRF-closed client additionally pins on-domain redirects and
	// blocks private dials (ssrf.go), which already classes youtube.com as a fixed v1 platform domain.
	youtubeBaseURL = "https://www.youtube.com"
	// A YouTube handle (the modern @identifier): 3–30 chars of [A-Za-z0-9._-]. We accept the bare
	// handle or a leading "@". Channel-ID (UC…) and legacy /c//user links are a deliberate later add —
	// the handle is the primary, user-facing identifier and keeps the path segment trivially safe.
	youtubeHandleMin = 3
	youtubeHandleMax = 30
)

// youtubeVerifier checks a YouTube channel's live status via the fixed youtube.com /@handle/live
// template, which — while the channel is broadcasting — lands (after an on-domain redirect) on the
// live watch page whose embedded player response carries the live-now flag. The HTTP client is
// injected so production uses the SSRF-closed client and tests point a plain client at an httptest
// server to exercise the fetch+parse path.
type youtubeVerifier struct {
	client  *http.Client
	baseURL string
	maxBody int64 // response-size cap (§7.4); defaults to maxBodyBytes, lowered in tests
}

func newYouTubeVerifier(client *http.Client, baseURL string) *youtubeVerifier {
	return &youtubeVerifier{client: client, baseURL: strings.TrimRight(baseURL, "/"), maxBody: maxBodyBytes}
}

// normalize validates a YouTube handle, accepting an optional leading "@". It returns the bare,
// lowercased handle (YouTube handles are case-insensitive) — 3–30 chars of [A-Za-z0-9._-], with no
// leading/trailing "." or "-" and no ".." run, so it stays a single, well-formed path segment. A URL,
// a slash, a space, or anything else is rejected — ("",false) — so the value can never escape its
// segment (SSRF-safe by construction, on top of the fixed host).
func (v *youtubeVerifier) normalize(channel string) (string, bool) {
	channel = strings.TrimSpace(channel)
	channel = strings.TrimPrefix(channel, "@")
	if len(channel) < youtubeHandleMin || len(channel) > youtubeHandleMax {
		return "", false
	}
	for _, r := range channel {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return "", false
		}
	}
	// Hygiene: reject a leading/trailing "." or "-" and any ".." run, so the handle is always a clean
	// single path segment (no relative-path oddities) and matches YouTube's own handle rules.
	if strings.HasPrefix(channel, ".") || strings.HasSuffix(channel, ".") ||
		strings.HasPrefix(channel, "-") || strings.HasSuffix(channel, "-") ||
		strings.Contains(channel, "..") {
		return "", false
	}
	return strings.ToLower(channel), true
}

// watchURL is the public "watch live" link for the guest (D-29) — the channel page, which surfaces
// the live stream while live — or "" if the handle is invalid.
func (v *youtubeVerifier) watchURL(channel string) string {
	norm, ok := v.normalize(channel)
	if !ok {
		return ""
	}
	return v.baseURL + "/@" + norm
}

// verify fetches the channel's /@handle/live page through the (SSRF-closed) client — which lands on
// the live watch page while the channel is broadcasting — and parses its live-now signal. Any failure
// — invalid handle, dial blocked, transport error, non-200, oversize/short read — degrades to
// StatusUnavailable (D-24), never an error.
func (v *youtubeVerifier) verify(ctx context.Context, channel string) Status {
	norm, ok := v.normalize(channel)
	if !ok {
		return StatusUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.baseURL+"/@"+norm+"/live", nil)
	if err != nil {
		return StatusUnavailable
	}
	// A plain, honest UA + HTML accept; no credentials, no cookies (best-effort + polite, §7.4).
	req.Header.Set("User-Agent", "guest-pass-livecheck/1.0 (+https://guest-pass.link)")
	req.Header.Set("Accept", "text/html")

	resp, err := v.client.Do(req)
	if err != nil {
		return StatusUnavailable // dial blocked (SSRF guard), timeout, off-domain redirect, etc.
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StatusUnavailable // 404 (no such channel) / 429 (rate-limited) / 5xx → can't tell
	}
	// Read one byte past the cap so an oversized body is detectable: a live marker past the truncation
	// point can't be trusted, so degrade rather than reporting a false "offline" (matches Twitch).
	body, err := io.ReadAll(io.LimitReader(resp.Body, v.maxBody+1))
	if err != nil {
		return StatusUnavailable
	}
	if int64(len(body)) > v.maxBody {
		return StatusUnavailable // oversized → can't reliably parse
	}
	return parseYouTubeLive(body)
}

// youtubeLiveRe matches YouTube's "broadcasting right now" flag from the watch page's embedded player
// response (liveBroadcastDetails.isLiveNow), tolerating arbitrary JSON whitespace around the colon.
// This is the live-NOW signal — true only while actually broadcasting — unlike isLiveContent /
// isLiveBroadcast, which stay true for a scheduled or already-ended stream, so it doesn't read an old
// VOD as live. Absence is read as offline (best-effort, fragile by nature, fixed via PR — D-29).
var youtubeLiveRe = regexp.MustCompile(`"isLiveNow"\s*:\s*true\b`)

func parseYouTubeLive(body []byte) Status {
	if youtubeLiveRe.Match(body) {
		return StatusLive
	}
	return StatusOffline
}
