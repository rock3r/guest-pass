package turn

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rock3r/guest-pass/internal/store"
)

func TestHostProvider_UsesOnlyTheConfiguredHostsRelay(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "turn.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	host, err := st.CreateHost(ctx, store.CreateHostParams{GoogleSub: "turn-host", Email: "turn@example.com", Name: "Turn", Status: store.HostActive})
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	cipher, err := NewSecretCipher("host-provider-token-secret-aaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("NewSecretCipher: %v", err)
	}
	secret, err := cipher.Encrypt("host-specific-shared-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := st.SetHostPreferences(ctx, store.HostPreferences{HostID: host.ID, Timezone: "UTC", MaxRes: 720, MaxFPS: 30, MaxBitrateKbps: 2500, CustomTURNEnabled: true, CustomTURNURL: "turns:host-turn.example:5349", CustomTURNSecretEncrypted: secret}); err != nil {
		t.Fatalf("SetHostPreferences: %v", err)
	}

	p := NewHostProvider("stun:stun.example:3478", "turns:instance-turn.example:5349", "instance-shared-secret", st, cipher)
	f, ok := p.ICEFrameFor(ctx, "peer-1", host.ID)
	if !ok || f.TTLSec == 0 {
		t.Fatalf("host ICE frame = %+v ok=%v, want a refreshable relay", f, ok)
	}
	var got string
	for _, server := range f.ICEServers {
		if server.Username != "" {
			got = server.URLs[0]
		}
	}
	if got != "turns:host-turn.example:5349" {
		t.Fatalf("TURN URL = %q, want the host-owned relay", got)
	}
}
