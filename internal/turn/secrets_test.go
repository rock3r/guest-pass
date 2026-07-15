package turn

import "testing"

func TestSecretCipher_RoundTripDoesNotExposePlaintext(t *testing.T) {
	cipher, err := NewSecretCipher("stable-token-secret-for-settings-encryption-aaaaaaaa")
	if err != nil {
		t.Fatalf("NewSecretCipher: %v", err)
	}
	const secret = "host-coturn-shared-secret"
	ciphertext, err := cipher.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext == secret {
		t.Fatal("ciphertext must not equal the supplied TURN secret")
	}
	got, err := cipher.Decrypt(ciphertext)
	if err != nil || got != secret {
		t.Fatalf("Decrypt = %q, %v; want %q, nil", got, err, secret)
	}
}
