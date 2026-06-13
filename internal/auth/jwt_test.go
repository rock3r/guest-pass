package auth

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testCurrent = "current-key-current-key-current-key-32b"
	testPrev    = "previous-key-previous-key-previous-32b"
)

var fixedNow = time.Unix(1_700_000_000, 0)

func newRing(t *testing.T, current string, previous ...string) *KeyRing {
	t.Helper()
	r, err := NewKeyRing(current, previous...)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	return r
}

func TestJWT_RoundTrip(t *testing.T) {
	r := newRing(t, testCurrent)
	tok, err := r.issueAt("host-123", time.Hour, fixedNow)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	hid, err := r.verifyAt(tok, fixedNow)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if hid != "host-123" {
		t.Errorf("host_id = %q, want host-123", hid)
	}
}

func TestJWT_Expired(t *testing.T) {
	r := newRing(t, testCurrent)
	tok, _ := r.issueAt("h", time.Hour, fixedNow)
	if _, err := r.verifyAt(tok, fixedNow.Add(2*time.Hour)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestJWT_TamperedRejected(t *testing.T) {
	r := newRing(t, testCurrent)
	tok, _ := r.issueAt("h", time.Hour, fixedNow)
	// Flip a byte in the payload segment.
	b := []byte(tok)
	if i := strings.IndexByte(tok, '.'); i >= 0 && i+1 < len(b) {
		b[i+1] ^= 0x01
	}
	if _, err := r.verifyAt(string(b), fixedNow); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid for tampered token, got %v", err)
	}
}

func TestJWT_KidRotation(t *testing.T) {
	// A token signed under the previous key still verifies while that key is in the ring.
	prevRing := newRing(t, testPrev)
	oldTok, _ := prevRing.issueAt("host-9", time.Hour, fixedNow)

	ring := newRing(t, testCurrent, testPrev) // current + previous
	hid, err := ring.verifyAt(oldTok, fixedNow)
	if err != nil || hid != "host-9" {
		t.Fatalf("old token under previous key: hid=%q err=%v", hid, err)
	}

	// Once the previous key is retired (dropped from the ring), the old token is invalid.
	retired := newRing(t, testCurrent)
	if _, err := retired.verifyAt(oldTok, fixedNow); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("retired-key token: expected ErrTokenInvalid, got %v", err)
	}
}

func TestJWT_AlgConfusionRejected(t *testing.T) {
	r := newRing(t, testCurrent)
	// A forged "alg":"none" token with no signature must be rejected.
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT","kid":"` + r.currentKid + `"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"host_id":"attacker","exp":9999999999}`))
	if _, err := r.verifyAt(hdr+"."+claims+".", fixedNow); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid for alg=none, got %v", err)
	}
}

func TestJWT_UnknownKidRejected(t *testing.T) {
	r := newRing(t, testCurrent)
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT","kid":"nope"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"host_id":"x","exp":9999999999}`))
	if _, err := r.verifyAt(hdr+"."+claims+".sig", fixedNow); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid for unknown kid, got %v", err)
	}
}

func TestJWT_MalformedRejected(t *testing.T) {
	r := newRing(t, testCurrent)
	for _, tok := range []string{"", "notatoken", "only.two", "a.b.c.d"} {
		if _, err := r.verifyAt(tok, fixedNow); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("verify(%q) = %v, want ErrTokenInvalid", tok, err)
		}
	}
}

func TestNewKeyRing_EmptyCurrentRejected(t *testing.T) {
	if _, err := NewKeyRing(""); err == nil {
		t.Fatal("expected error for empty current key")
	}
}
