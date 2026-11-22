package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

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
