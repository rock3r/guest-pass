package livecheck

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const (
	// twitchBaseURL is the FIXED template host (§7.4): the channel identifier is only ever appended
	// as a sanitized path segment, never the host — so a host-supplied value can't redirect the
	// fetch to an arbitrary address.
	twitchBaseURL = "https://www.twitch.tv"
	// twitchChannelMin/Max bound a Twitch username (login): 3–25 chars, [a-z0-9_].
	twitchChannelMin = 3
	twitchChannelMax = 25
	// maxBodyBytes caps the scraped response (§7.4 size cap). Twitch channel pages are large; 4 MiB
	// is generous headroom while still bounding a hostile/huge response.
	maxBodyBytes = 4 << 20
)

// twitchVerifier checks a Twitch channel's live status via the fixed twitch.tv channel-page
// template. The HTTP client is injected so production uses the SSRF-closed client and tests can
// point a plain client at an httptest server to exercise the parse path.
type twitchVerifier struct {
	client  *http.Client
	baseURL string
	maxBody int64 // response-size cap (§7.4); defaults to maxBodyBytes, lowered in tests
}

func newTwitchVerifier(client *http.Client, baseURL string) *twitchVerifier {
	return &twitchVerifier{client: client, baseURL: strings.TrimRight(baseURL, "/"), maxBody: maxBodyBytes}
}

// normalize validates + lowercases a Twitch channel login. It accepts ONLY a bare identifier
// (3–25 chars of [A-Za-z0-9_]); a URL, "@handle", or anything with a slash/dot/space is rejected,
// so the value can never escape its path segment (SSRF-safe by construction). Returns ("",false)
// when invalid.
func (v *twitchVerifier) normalize(channel string) (string, bool) {
	channel = strings.TrimSpace(channel)
	if len(channel) < twitchChannelMin || len(channel) > twitchChannelMax {
		return "", false
	}
	for _, r := range channel {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return "", false
		}
	}
	return strings.ToLower(channel), true
}

// watchURL is the public "watch live" link for the guest (D-29), or "" if the channel is invalid.
func (v *twitchVerifier) watchURL(channel string) string {
	norm, ok := v.normalize(channel)
	if !ok {
		return ""
	}
	return v.baseURL + "/" + norm
}

// verify fetches the channel page through the (SSRF-closed) client and parses its live signal. Any
// failure — invalid channel, dial blocked, transport error, non-200, oversize/short read — degrades
// to StatusUnavailable (D-24), never an error.
func (v *twitchVerifier) verify(ctx context.Context, channel string) Status {
	norm, ok := v.normalize(channel)
	if !ok {
		return StatusUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.baseURL+"/"+norm, nil)
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
	// Read one byte past the cap so an oversized body is detectable: if it read more than maxBody
	// the page was truncated and a missing live marker can't be trusted — degrade to unavailable
	// rather than reporting a false "offline" (codex). A within-cap body parses normally.
	body, err := io.ReadAll(io.LimitReader(resp.Body, v.maxBody+1))
	if err != nil {
		return StatusUnavailable
	}
	if int64(len(body)) > v.maxBody {
		return StatusUnavailable // oversized → can't reliably parse
	}
	return parseTwitchLive(body)
}

// liveSignalRe matches the schema.org live flag tolerating arbitrary JSON whitespace around the
// colon (and around the boolean), so a harmless formatting change — `"isLiveBroadcast" : true`, a
// newline after the colon — doesn't make a live channel read as offline (codex). JSON permits any
// whitespace at separators.
var liveSignalRe = regexp.MustCompile(`"isLiveBroadcast"\s*:\s*true\b`)

// parseTwitchLive is the best-effort live-signal parse (D-29 — fragile/ToS-grey, fixed via PR): a
// live Twitch channel page embeds schema.org markup whose isLiveBroadcast flag is true. Absence is
// read as offline.
func parseTwitchLive(body []byte) Status {
	if liveSignalRe.Match(body) {
		return StatusLive
	}
	return StatusOffline
}
