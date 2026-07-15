package turn

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// SecretCipher encrypts host-supplied TURN shared secrets before SQLite persistence. The key is
// deterministically derived from TOKEN_SECRET, which is intentionally stable for the deployment;
// neither raw TURN secrets nor the derived key are ever rendered, exported, or logged.
type SecretCipher struct{ aead cipher.AEAD }

func NewSecretCipher(keyMaterial string) (*SecretCipher, error) {
	if len(keyMaterial) < 32 {
		return nil, errors.New("settings key material is too short")
	}
	key := sha256.Sum256([]byte("guestpass/host-turn-secret/v1\x00" + keyMaterial))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretCipher{aead: aead}, nil
}

func (c *SecretCipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *SecretCipher) Decrypt(encoded string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if len(sealed) < c.aead.NonceSize() {
		return "", errors.New("ciphertext is truncated")
	}
	plain, err := c.aead.Open(nil, sealed[:c.aead.NonceSize()], sealed[c.aead.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
