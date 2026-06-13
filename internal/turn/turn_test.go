package turn

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testTURNSecret = "turn-test-secret-aaaaaaaaaaaaaaaaaaaa"

// recompute the expected coturn REST credential for a username, to verify a mint.
func wantCredential(secret, username string) string {
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// EN-4 / PD-4: the ephemeral credential is the coturn REST shape —
// username = "<expiryUnix>:<peerId>", credential = base64(HMAC-SHA1(secret, username)).
func TestProvider_MintFormatAndVerify(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	p := NewProvider("", "turns:turn.example.org:5349", testTURNSecret)
	p.now = fixedClock(now)

	username, credential := p.mint("peer-7", now)

	expiry, peer, found := strings.Cut(username, ":")
	if !found || peer != "peer-7" {
		t.Fatalf("username = %q, want \"<expiry>:peer-7\"", username)
	}
	exp, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		t.Fatalf("expiry %q not an int: %v", expiry, err)
	}
	if want := now.Add(p.ttl).Unix(); exp != want {
		t.Fatalf("expiry = %d, want now+ttl = %d", exp, want)
	}
	if credential != wantCredential(testTURNSecret, username) {
		t.Fatalf("credential does not verify against the secret")
	}
}

// TTL stays inside coturn's recommended ephemeral band so a kick revokes relay access
// within one TTL (EN-4 / PD-4: 60–120s).
func TestProvider_TTLWithinBand(t *testing.T) {
	p := NewProvider("", "turns:turn.example.org:5349", testTURNSecret)
	if ttl := p.TTLSeconds(); ttl < 60 || ttl > 120 {
		t.Fatalf("TTLSeconds = %d, want within [60,120]", ttl)
	}
}

// An ice-refresh re-mint issues a DISTINCT, still-valid credential (the later expiry
// changes the username, hence the HMAC).
func TestProvider_RefreshIssuesDistinctStillValidCred(t *testing.T) {
	p := NewProvider("", "turns:turn.example.org:5349", testTURNSecret)
	t0 := time.Unix(1_700_000_000, 0)
	u1, c1 := p.mint("peer-7", t0)
	u2, c2 := p.mint("peer-7", t0.Add(2*time.Second))
	if u1 == u2 || c1 == c2 {
		t.Fatalf("refresh should change the credential: u1=%q u2=%q", u1, u2)
	}
	if c1 != wantCredential(testTURNSecret, u1) || c2 != wantCredential(testTURNSecret, u2) {
		t.Fatalf("both credentials must verify against the secret")
	}
}

// STUN-only (no TURN configured): one STUN entry, no creds, no TTL, no TURN entry — the
// M1 behavior is unchanged (D-38).
func TestProvider_STUNOnlyUnchanged(t *testing.T) {
	p := NewProvider("stun:stun.example.org:3478", "", "")
	servers := p.ICEServers("peer-7")
	if len(servers) != 1 || len(servers[0].URLs) != 1 || servers[0].URLs[0] != "stun:stun.example.org:3478" {
		t.Fatalf("ICEServers = %+v, want one STUN entry", servers)
	}
	if servers[0].Username != "" || servers[0].Credential != "" {
		t.Fatalf("STUN entry must carry no creds, got %+v", servers[0])
	}
	if p.TTLSeconds() != 0 {
		t.Fatalf("STUN-only TTLSeconds = %d, want 0", p.TTLSeconds())
	}
	f, ok := p.ICEFrame("peer-7")
	if !ok || f.TTLSec != 0 {
		t.Fatalf("STUN-only ICE frame = %+v ok=%v, want a frame with no ttlSec", f, ok)
	}
}

// When TURN is configured the join-ack carries STUN + a TURN entry with a fresh ephemeral
// credential and a ttlSec (AC-4).
func TestProvider_ICEFrameCarriesTURN(t *testing.T) {
	p := NewProvider("stun:stun.example.org:3478", "turns:turn.example.org:5349", testTURNSecret)
	f, ok := p.ICEFrame("peer-7")
	if !ok {
		t.Fatal("expected an ICE frame")
	}
	if f.T != "ice" || f.TTLSec <= 0 {
		t.Fatalf("ice frame = %+v, want t=ice + positive ttlSec", f)
	}
	var gotURL, gotUser, gotCred string
	for _, s := range f.ICEServers {
		if len(s.URLs) == 1 && s.URLs[0] == "turns:turn.example.org:5349" {
			gotURL, gotUser, gotCred = s.URLs[0], s.Username, s.Credential
		}
	}
	if gotURL == "" || gotUser == "" || gotCred == "" {
		t.Fatalf("ice frame missing a credentialled TURN entry: %+v", f.ICEServers)
	}
}

// Dev / loopback with neither STUN nor TURN: no ICE servers, and the caller is told to
// skip the join-ack entirely.
func TestProvider_EmptyWhenUnconfigured(t *testing.T) {
	p := NewProvider("", "", "")
	if got := p.ICEServers("peer-7"); got != nil {
		t.Fatalf("ICEServers = %+v, want nil", got)
	}
	if _, ok := p.ICEFrame("peer-7"); ok {
		t.Fatal("ICEFrame ok should be false when nothing is configured")
	}
}

// The marshaled entries match the browser RTCIceServer dictionary so the client can pass
// them straight to RTCPeerConnection.
func TestProvider_MarshalsToRTCIceServerShape(t *testing.T) {
	p := NewProvider("stun:stun.example.org:3478", "", "")
	b, err := json.Marshal(p.ICEServers("peer-7"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b), `[{"urls":["stun:stun.example.org:3478"]}]`; got != want {
		t.Fatalf("marshal = %s, want %s", got, want)
	}
}
