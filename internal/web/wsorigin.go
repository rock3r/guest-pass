package web

import (
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

// WSOrigin authorizes browser Origins against the public BASE_URL host so a
// reverse proxy or tunnel that rewrites Host (cloudflared → guestpass:8137)
// does not reject same-origin host/guest handshakes. Cross-origin Origins stay
// rejected by coder/websocket. No InsecureSkipVerify.
type WSOrigin struct{}

// Patterns returns the request-origin hosts Accept should authorize, derived
// solely from BASE_URL. Empty or unparseable values yield no extra hosts, so
// the library's default (Origin host == request Host) remains in force.
func (WSOrigin) Patterns(baseURL string) []string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return nil
	}
	return []string{u.Host}
}

// AcceptOptions builds the handshake options for a /ws upgrade. The request
// Host is always implicitly allowed; Patterns adds the public origin.
func (WSOrigin) AcceptOptions(baseURL string) *websocket.AcceptOptions {
	return &websocket.AcceptOptions{OriginPatterns: WSOrigin{}.Patterns(baseURL)}
}
