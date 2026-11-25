package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

type sealer struct {
	aead cipher.AEAD
}

func newSealer(key []byte) (*sealer, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("encryption key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

// seal returns nonce||ciphertext. The record label is bound as additional data so a ciphertext
// copied onto another row does not decrypt.
func (s *sealer) seal(plain, label string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(plain), []byte(label)), nil
}

func (s *sealer) open(blob []byte, label string) (string, error) {
	n := s.aead.NonceSize()
	if len(blob) < n+s.aead.Overhead() {
		return "", errors.New("ciphertext too short")
	}
	plain, err := s.aead.Open(nil, blob[:n], blob[n:], []byte(label))
	if err != nil {
		return "", errors.New("authentication failed (wrong key or corrupted value)")
	}
	return string(plain), nil
}
