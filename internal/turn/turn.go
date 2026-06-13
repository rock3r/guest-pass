// Package turn assembles the per-peer WebRTC ICE configuration the signaling server hands
// each connection in the {t:"ice"} join-ack (AD-14). v1 is STUN-only by default (D-38):
// the public instance runs no relay. When a relay is configured (TURN_URL/TURN_SECRET) a
// TURN entry is added, carrying a short-lived ephemeral HMAC credential (EN-4) minted per
// peer. The credential must ride the revocable signaling WS — not a long-lived REST API —
// so a kick/disconnect revokes relay access within one credential TTL.
package turn

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/rock3r/guest-pass/internal/signaling"
)

// CredTTL is the lifetime of a minted ephemeral TURN credential. It sits inside coturn's
// recommended ephemeral band (60–120s, PD-4) — short enough that a kick revokes relay
// access within one TTL (EN-4), long enough that the client comfortably refreshes before
// expiry via {t:ice-refresh}.
const CredTTL = 90 * time.Second

// Provider builds the ICE configuration for a peer. STUN is offered whenever STUN_URL is
// set; a TURN entry with a fresh ephemeral credential is added when both TURN_URL and
// TURN_SECRET are set. It is safe for concurrent use (the fields are read-only after
// construction; now is a pure clock).
type Provider struct {
	stunURL    string
	turnURL    string
	turnSecret []byte
	ttl        time.Duration
	now        func() time.Time
}

// NewProvider builds a Provider from the configured STUN/TURN settings. Empty STUN or TURN
// values simply omit that entry (STUN-only, or no servers at all in dev/loopback).
func NewProvider(stunURL, turnURL, turnSecret string) *Provider {
	return &Provider{
		stunURL:    strings.TrimSpace(stunURL),
		turnURL:    strings.TrimSpace(turnURL),
		turnSecret: []byte(turnSecret),
		ttl:        CredTTL,
		now:        time.Now,
	}
}

// turnEnabled reports whether a relay credential can be minted.
func (p *Provider) turnEnabled() bool { return p.turnURL != "" && len(p.turnSecret) > 0 }

// mint builds an ephemeral coturn REST credential for peerID, valid for the provider TTL
// from now: username = "<expiryUnix>:<peerId>", credential = base64(HMAC-SHA1(secret,
// username)). Embedding the peer id binds the credential to the connection.
func (p *Provider) mint(peerID string, now time.Time) (username, credential string) {
	expiry := now.Add(p.ttl).Unix()
	username = strconv.FormatInt(expiry, 10) + ":" + peerID
	m := hmac.New(sha1.New, p.turnSecret)
	m.Write([]byte(username))
	return username, base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// ICEServers builds the ICE server list for peerID: the STUN entry (no creds) when
// configured, plus a TURN entry with a freshly-minted ephemeral credential when a relay
// is configured. Returns nil when nothing is configured (dev/loopback, D-38).
func (p *Provider) ICEServers(peerID string) []signaling.ICEServer {
	var out []signaling.ICEServer
	if p.stunURL != "" {
		out = append(out, signaling.ICEServer{URLs: []string{p.stunURL}})
	}
	if p.turnEnabled() {
		u, c := p.mint(peerID, p.now())
		out = append(out, signaling.ICEServer{URLs: []string{p.turnURL}, Username: u, Credential: c})
	}
	return out
}

// TTLSeconds is the credential lifetime in seconds, for the client to schedule an
// {t:ice-refresh} before expiry. It is 0 when no TURN is configured (nothing to refresh).
func (p *Provider) TTLSeconds() int {
	if !p.turnEnabled() {
		return 0
	}
	return int(p.ttl / time.Second)
}

// ICEFrame builds the {t:"ice"} join-ack for peerID, with ttlSec set only when a
// refreshable TURN credential is present. ok is false when no ICE servers are configured
// at all, so the caller skips the join-ack entirely.
func (p *Provider) ICEFrame(peerID string) (signaling.Frame, bool) {
	servers := p.ICEServers(peerID)
	if len(servers) == 0 {
		return signaling.Frame{}, false
	}
	return signaling.Frame{T: "ice", ICEServers: servers, TTLSec: p.TTLSeconds()}, true
}
