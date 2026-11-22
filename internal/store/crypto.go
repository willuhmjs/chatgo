package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// providerKeyInfo domain-separates the provider API-key at-rest key inside
// HKDF so it cannot collide with any other key derived from SESSION_SECRET.
var providerKeyInfo = []byte("chatgo:providers-atrest/v1")

// deriveKey derives the AES-256-GCM key that seals provider API keys at rest:
// HKDF-SHA256(secret, salt=nil, info="chatgo:providers-atrest/v1").
// Pre-1.0: blobs sealed under the old raw SHA-256 derivation no longer
// decrypt; those rows surface with Provider.KeyUnreadable instead of failing
// List/Get (see providers.go scanWithKey).
func deriveKey(secret []byte) ([]byte, error) {
	key := make([]byte, 32)
	r := hkdf.New(sha256.New, secret, nil, providerKeyInfo)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("store: derive at-rest key: %w", err)
	}
	return key, nil
}

// encrypt seals plaintext with AES-256-GCM using the store's derived key.
// Output layout: nonce(12) || ciphertext||tag.
func (s *Store) encrypt(plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// decrypt reverses encrypt; returns an error if the blob was not produced
// with the current SESSION_SECRET.
func (s *Store) decrypt(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("store: ciphertext too short")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("store: decrypt failed (SESSION_SECRET changed?): %w", err)
	}
	return string(plain), nil
}
