package store

import (
	"context"
	"testing"
)

func TestHostPreferences_DefaultsAndRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	host, err := st.CreateHost(ctx, CreateHostParams{
		GoogleSub: "prefs-sub", Email: "prefs@example.com", Name: "Preferences", Status: HostActive,
	})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	got, err := st.GetHostPreferences(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHostPreferences defaults: %v", err)
	}
	if got.Timezone != "UTC" || got.MaxRes != DefaultMaxRes || got.MaxFPS != DefaultMaxFPS || got.MaxBitrateKbps != DefaultMaxBitrateKbps {
		t.Fatalf("defaults = %+v, want UTC and %d/%d/%d", got, DefaultMaxRes, DefaultMaxFPS, DefaultMaxBitrateKbps)
	}

	want := HostPreferences{
		HostID:                    host.ID,
		Timezone:                  "Europe/Rome",
		YouTubeChannel:            "guestpasslive",
		TwitchChannel:             "guestpass",
		DefaultChannelPlatform:    "youtube",
		MaxRes:                    1080,
		MaxFPS:                    60,
		MaxBitrateKbps:            4500,
		CustomTURNEnabled:         true,
		CustomTURNURL:             "turns:turn.example.test:5349?transport=tcp",
		CustomTURNSecretEncrypted: "opaque-ciphertext",
	}
	if err := st.SetHostPreferences(ctx, want); err != nil {
		t.Fatalf("SetHostPreferences: %v", err)
	}
	got, err = st.GetHostPreferences(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHostPreferences saved: %v", err)
	}
	if *got != want {
		t.Fatalf("preferences = %+v, want %+v", got, want)
	}
}
