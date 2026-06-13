// Package turn assembles the WebRTC ICE configuration the signaling server hands each
// peer in the {t:"ice"} join-ack (AD-14). v1 is STUN-only by default (D-38): the public
// instance runs no relay. A TURN entry — with its short-lived ephemeral HMAC credential
// (EN-4) — is added in M2 when a relay is configured; that credential must ride the
// revocable signaling WS so a kick revokes relay access within the credential TTL.
package turn

import (
	"strings"

	"github.com/rock3r/guest-pass/internal/signaling"
)

// ICEServers builds the ICE server list from the configured STUN URL. When stunURL is
// empty (dev / loopback, where peers connect on host-local candidates) the list is nil —
// the client then runs with no servers. STUN entries carry no credentials.
func ICEServers(stunURL string) []signaling.ICEServer {
	stunURL = strings.TrimSpace(stunURL)
	if stunURL == "" {
		return nil
	}
	return []signaling.ICEServer{{URLs: []string{stunURL}}}
}
