package turn

import (
	"context"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// PreferenceStore is the narrow persistence seam used to resolve a host's optional relay.
type PreferenceStore interface {
	GetHostPreferences(context.Context, string) (*store.HostPreferences, error)
}

// HostProvider chooses a host's encrypted BYO-TURN setting when enabled, otherwise preserving the
// instance-wide provider. The browser still receives only a short-lived HMAC credential.
type HostProvider struct {
	stunURL  string
	fallback *Provider
	prefs    PreferenceStore
	cipher   interface{ Decrypt(string) (string, error) }
}

func NewHostProvider(stunURL, turnURL, turnSecret string, prefs PreferenceStore, cipher interface{ Decrypt(string) (string, error) }) *HostProvider {
	return &HostProvider{stunURL: stunURL, fallback: NewProvider(stunURL, turnURL, turnSecret), prefs: prefs, cipher: cipher}
}

// ICEFrame keeps compatibility with the original global provider when a caller has no host scope.
func (p *HostProvider) ICEFrame(peerID string) (signaling.Frame, bool) {
	return p.fallback.ICEFrame(peerID)
}

// ICEFrameFor resolves a host-specific relay for a credentialed connection. A corrupted custom
// setting falls back to STUN-only instead of leaking a secret or crossing host boundaries.
func (p *HostProvider) ICEFrameFor(ctx context.Context, peerID, hostID string) (signaling.Frame, bool) {
	if p.prefs == nil || p.cipher == nil {
		return p.fallback.ICEFrame(peerID)
	}
	prefs, err := p.prefs.GetHostPreferences(ctx, hostID)
	if err != nil || !prefs.CustomTURNEnabled || prefs.CustomTURNURL == "" || prefs.CustomTURNSecretEncrypted == "" {
		return p.fallback.ICEFrame(peerID)
	}
	secret, err := p.cipher.Decrypt(prefs.CustomTURNSecretEncrypted)
	if err != nil || secret == "" {
		return NewProvider(p.stunURL, "", "").ICEFrame(peerID)
	}
	return NewProvider(p.stunURL, prefs.CustomTURNURL, secret).ICEFrame(peerID)
}
