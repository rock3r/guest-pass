package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rock3r/guest-pass/internal/livecheck"
)

// fakeLiveChecker delegates the PURE helpers (WatchURL/Normalize) to a real livecheck.Checker (no
// network) and returns a canned Check result — so the link form + watch link exercise the real
// Twitch rules while the live-status endpoint is tested without hitting Twitch.
type fakeLiveChecker struct {
	real   *livecheck.Checker
	result livecheck.Result
}

func newFakeLiveChecker() *fakeLiveChecker {
	return &fakeLiveChecker{
		real:   livecheck.NewChecker(),
		result: livecheck.Result{Status: livecheck.StatusLive, WatchURL: "https://www.twitch.tv/ninja", CheckedAt: 1},
	}
}

func (f *fakeLiveChecker) Check(context.Context, string, string) livecheck.Result { return f.result }
func (f *fakeLiveChecker) WatchURL(p, c string) string                            { return f.real.WatchURL(p, c) }
func (f *fakeLiveChecker) Normalize(p, c string) (string, bool)                   { return f.real.Normalize(p, c) }

// Linking a Twitch channel persists the (platform, channel) pair; the stream-detail page then shows
// it + the watch link. An invalid channel is rejected (?error=channel) and persists nothing. (AC-8)
func TestLiveCheck_LinkChannel(t *testing.T) {
	a := newAPIHarness(t)
	host, cookie := a.host(t, "linker")
	streamID := a.createStream(t, cookie, "Linked Show")

	// Invalid channel → rejected, nothing stored.
	rec := a.formPost(t, "/app/streams/"+streamID+"/channel", "platform=twitch&channel=bad+name", cookie)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=channel") {
		t.Fatalf("invalid channel = %d loc=%q, want 303 → ?error=channel", rec.Code, rec.Header().Get("Location"))
	}
	if s, _ := a.store.GetStream(context.Background(), streamID); s.TwitchYTChannel != nil {
		t.Fatalf("invalid channel must not persist, got %v", s.TwitchYTChannel)
	}

	// Valid channel → persisted (normalized, lowercased).
	rec = a.formPost(t, "/app/streams/"+streamID+"/channel", "platform=twitch&channel=Ninja", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("link = %d", rec.Code)
	}
	s, _ := a.store.GetStream(context.Background(), streamID)
	if s.TwitchYTPlatform == nil || *s.TwitchYTPlatform != "twitch" || s.TwitchYTChannel == nil || *s.TwitchYTChannel != "ninja" {
		t.Fatalf("after link: platform=%v channel=%v", s.TwitchYTPlatform, s.TwitchYTChannel)
	}
	_ = host

	// The stream-detail page shows the linked channel + the watch link.
	rec = a.req(t, http.MethodGet, "/app/streams/"+streamID, "", cookie)
	body := rec.Body.String()
	if !strings.Contains(body, "twitch/ninja") || !strings.Contains(body, "https://www.twitch.tv/ninja") {
		t.Fatalf("stream-detail missing the linked channel / watch link:\n%s", body)
	}

	// Unlink (empty channel) clears it.
	rec = a.formPost(t, "/app/streams/"+streamID+"/channel", "platform=twitch&channel=", cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unlink = %d", rec.Code)
	}
	if s, _ := a.store.GetStream(context.Background(), streamID); s.TwitchYTChannel != nil {
		t.Fatalf("unlink must clear the channel, got %v", s.TwitchYTChannel)
	}
}

// The guest pass page surfaces the public watch-live link once a channel is linked (D-29/AC-8).
func TestLiveCheck_PassPageWatchLink(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "watchhost")
	streamID := a.createStream(t, cookie, "Watch Show")
	_, raw := a.mintPass(t, streamID, "Dana")

	// No channel linked → no watch link.
	if body := a.req(t, http.MethodGet, "/p/"+raw, "", cookie).Body.String(); strings.Contains(body, "watch-live") {
		t.Fatal("no channel linked, but the pass page shows a watch link")
	}

	plat, ch := "twitch", "ninja"
	if err := a.store.SetStreamChannel(context.Background(), streamID, &plat, &ch); err != nil {
		t.Fatalf("link channel: %v", err)
	}
	body := a.req(t, http.MethodGet, "/p/"+raw, "", nil).Body.String()
	if !strings.Contains(body, "https://www.twitch.tv/ninja") {
		t.Fatalf("pass page missing the watch-live link after linking:\n%s", body)
	}
}

// The host livecheck endpoint returns the verified-live status + watch link for a linked channel,
// and linked=false (no fetch) when none is linked (the D-24 fold source, AC-8).
func TestLiveCheck_StatusEndpoint(t *testing.T) {
	a := newAPIHarness(t)
	_, cookie := a.host(t, "statushost")
	streamID := a.createStream(t, cookie, "Status Show")

	// No channel → linked:false, status-unavailable.
	rec := a.req(t, http.MethodGet, "/api/streams/"+streamID+"/livecheck", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("livecheck (unlinked) = %d", rec.Code)
	}
	var v livecheckStatusView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Linked || v.Status != string(livecheck.StatusUnavailable) {
		t.Fatalf("unlinked livecheck = %+v, want linked:false unavailable", v)
	}

	// Linked → the canned fake result (live + watch URL).
	plat, ch := "twitch", "ninja"
	_ = a.store.SetStreamChannel(context.Background(), streamID, &plat, &ch)
	rec = a.req(t, http.MethodGet, "/api/streams/"+streamID+"/livecheck", "", cookie)
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.Linked || v.Status != string(livecheck.StatusLive) || v.WatchURL != "https://www.twitch.tv/ninja" || v.Platform != "twitch" {
		t.Fatalf("linked livecheck = %+v, want live + watch url", v)
	}

	// Auth required + ownership: another host's stream is 404, an anonymous request is 401.
	other, otherCookie := a.host(t, "status-other")
	_ = other
	if rec := a.req(t, http.MethodGet, "/api/streams/"+streamID+"/livecheck", "", otherCookie); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign-stream livecheck = %d, want 404", rec.Code)
	}
	if rec := a.req(t, http.MethodGet, "/api/streams/"+streamID+"/livecheck", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon livecheck = %d, want 401", rec.Code)
	}
}
