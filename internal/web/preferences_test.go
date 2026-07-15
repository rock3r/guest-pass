package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rock3r/guest-pass/internal/store"
)

func TestApp_HostPreferencesPersistAndApplyToNewStreams(t *testing.T) {
	a := newAPIHarness(t)
	host, cookie := a.host(t, "preferences")

	form := url.Values{
		"timezone":                 {"Europe/Rome"},
		"youtube_channel":          {"@GuestPassLive"},
		"twitch_channel":           {"GuestPassTV"},
		"default_channel_platform": {"youtube"},
		"max_res":                  {"1080"},
		"max_fps":                  {"60"},
		"max_bitrate_kbps":         {"4500"},
	}
	rec := a.formReq(t, http.MethodPost, "/app/settings/preferences", form, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app/settings?preferences=saved" {
		t.Fatalf("save preferences = %d location=%q, want 303 to saved settings", rec.Code, rec.Header().Get("Location"))
	}

	prefs, err := a.store.GetHostPreferences(context.Background(), host.ID)
	if err != nil {
		t.Fatalf("GetHostPreferences: %v", err)
	}
	if prefs.Timezone != "Europe/Rome" || prefs.YouTubeChannel != "guestpasslive" || prefs.TwitchChannel != "guestpasstv" || prefs.DefaultChannelPlatform != "youtube" {
		t.Fatalf("saved preferences = %+v", prefs)
	}
	if prefs.MaxRes != 1080 || prefs.MaxFPS != 60 || prefs.MaxBitrateKbps != 4500 {
		t.Fatalf("saved quality defaults = %d/%d/%d, want 1080/60/4500", prefs.MaxRes, prefs.MaxFPS, prefs.MaxBitrateKbps)
	}
	for _, path := range []string{"/app", "/app/calendar"} {
		rec = a.req(t, http.MethodGet, path, "", cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Scheduled start (Europe/Rome)") {
			t.Fatalf("%s does not identify the saved schedule timezone; body:\n%s", path, body)
		}
	}

	rec = a.formReq(t, http.MethodPost, "/app/streams", url.Values{
		"title":        {"Rome default stream"},
		"scheduled_at": {"2026-07-01T15:30"},
	}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create stream = %d body=%s", rec.Code, rec.Body.String())
	}
	streams, err := a.store.ListStreamsByHost(context.Background(), host.ID)
	if err != nil || len(streams) != 1 {
		t.Fatalf("ListStreamsByHost = %v streams=%d", err, len(streams))
	}
	stream := streams[0]
	if stream.ScheduledAt == nil || *stream.ScheduledAt != time.Date(2026, 7, 1, 13, 30, 0, 0, time.UTC).Unix() {
		t.Fatalf("scheduled_at = %v, want 15:30 Europe/Rome stored as UTC", stream.ScheduledAt)
	}
	if stream.MaxRes == nil || *stream.MaxRes != 1080 || stream.MaxFPS == nil || *stream.MaxFPS != 60 || stream.MaxBitrateKbps == nil || *stream.MaxBitrateKbps != 4500 {
		t.Fatalf("stream quality defaults = %+v, want 1080/60/4500", stream)
	}
	if stream.TwitchYTPlatform == nil || *stream.TwitchYTPlatform != "youtube" || stream.TwitchYTChannel == nil || *stream.TwitchYTChannel != "guestpasslive" {
		t.Fatalf("stream channel default = platform=%v channel=%v", stream.TwitchYTPlatform, stream.TwitchYTChannel)
	}
	rec = a.req(t, http.MethodPost, "/api/streams", `{"title":"API default stream"}`, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("API create stream = %d body=%s", rec.Code, rec.Body.String())
	}
	streams, err = a.store.ListStreamsByHost(context.Background(), host.ID)
	if err != nil || len(streams) != 2 {
		t.Fatalf("ListStreamsByHost after API create = %v streams=%d", err, len(streams))
	}
	apiStream := streams[0]
	if apiStream.Title == stream.Title {
		apiStream = streams[1]
	}
	if apiStream.MaxRes == nil || *apiStream.MaxRes != 1080 || apiStream.MaxFPS == nil || *apiStream.MaxFPS != 60 || apiStream.MaxBitrateKbps == nil || *apiStream.MaxBitrateKbps != 4500 || apiStream.TwitchYTChannel == nil || *apiStream.TwitchYTChannel != "guestpasslive" {
		t.Fatalf("API stream did not receive host defaults: %+v", apiStream)
	}
	rec = a.req(t, http.MethodGet, "/app/streams/"+stream.ID+"/edit", "", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Scheduled start (Europe/Rome)") {
		t.Fatalf("edit form must identify its schedule timezone: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestApp_DashboardUsesHostTimezoneForScheduleTiles(t *testing.T) {
	a := newAPIHarness(t)
	host, cookie := a.host(t, "dashboard-timezone")
	if err := a.store.SetHostPreferences(context.Background(), store.HostPreferences{HostID: host.ID, Timezone: "America/Los_Angeles", MaxRes: 720, MaxFPS: 30, MaxBitrateKbps: 2500}); err != nil {
		t.Fatalf("SetHostPreferences: %v", err)
	}
	scheduled := time.Date(2026, time.August, 1, 6, 30, 0, 0, time.UTC).Unix() // Jul 31, 23:30 PDT
	if _, err := a.store.CreateStream(context.Background(), store.CreateStreamParams{HostID: host.ID, Title: "Late show", Status: store.StreamScheduled, ScheduledAt: &scheduled}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	rec := a.req(t, http.MethodGet, "/app", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Jul", "31", "23:30 PDT"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard missing host-local schedule value %q; body:\n%s", want, body)
		}
	}
}

func TestValidTURNURL_AcceptsStandardICEURLForms(t *testing.T) {
	for _, raw := range []string{
		"turn:turn.example.com:3478",
		"turns:turn.example.com:5349?transport=tcp",
		"turn://turn.example.com:3478",
		"turns://turn.example.com:5349?transport=tcp",
		"turns:[2001:db8::1]:5349",
	} {
		if !validTURNURL(raw) {
			t.Errorf("validTURNURL(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{"", "https://turn.example.com", "turn:", "turn::", "turn:turn.example.com:", "turns://?transport=tcp", "turn:user@turn.example.com:3478"} {
		if validTURNURL(raw) {
			t.Errorf("validTURNURL(%q) = true, want false", raw)
		}
	}
}
