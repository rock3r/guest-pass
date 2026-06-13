package token

import (
	"strings"
	"testing"
)

func TestMint_UniqueAndOpaque(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := Mint()
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		if len(tok) < 20 { // 16 bytes base64url ≈ 22 chars
			t.Fatalf("token too short: %q", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token minted: %q", tok)
		}
		seen[tok] = true
	}
}

func TestHasher_HashAndEqual(t *testing.T) {
	h, err := NewHasher("a-stable-token-secret-aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("NewHasher: %v", err)
	}
	raw, _ := Mint()
	stored := h.Hash(raw)

	if strings.Contains(stored, raw) {
		t.Error("stored hash must not contain the raw token")
	}
	if !h.Equal(raw, stored) {
		t.Error("Equal should accept the matching token")
	}
	if h.Equal("not-the-token", stored) {
		t.Error("Equal should reject a different token")
	}
	if h.Equal(raw, "not-base64-$$$") {
		t.Error("Equal should reject a malformed stored hash")
	}
	if h.Equal(raw, "") {
		t.Error("Equal should reject an empty stored hash")
	}
}

func TestHasher_DiffersBySecret(t *testing.T) {
	h1, _ := NewHasher("secret-one-secret-one-secret-one-1234")
	h2, _ := NewHasher("secret-two-secret-two-secret-two-1234")
	raw, _ := Mint()
	if h1.Hash(raw) == h2.Hash(raw) {
		t.Error("different secrets must produce different hashes")
	}
	// A hash made under one secret must not validate under another (rotation safety).
	if h2.Equal(raw, h1.Hash(raw)) {
		t.Error("a hash from a different secret must not validate")
	}
}

func TestNewHasher_RejectsEmpty(t *testing.T) {
	if _, err := NewHasher(""); err == nil {
		t.Fatal("expected error for empty secret")
	}
}
