package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// cipherVersion prefixes every ciphertext so that the format can evolve
// (for example to a rotated key) without ambiguity.
const cipherVersion byte = 0x01

// ErrDecrypt is returned when a ciphertext is malformed, was produced with a
// different key, or was tampered with.
var ErrDecrypt = errors.New("store: decryption failed: ciphertext is invalid or was modified")

// Cipher encrypts secrets at rest with AES-256-GCM keyed by the master key.
//
// Ciphertext layout: version(1) || nonce(12) || sealed(plaintext) || tag(16).
// The associated data binds a ciphertext to its owner (for example tenant,
// provider, account, and column) so a ciphertext copied to another row fails
// authentication.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a Cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("store: master key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("store: init AES: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("store: init GCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext with a fresh random nonce. aad may be nil.
func (c *Cipher) Encrypt(plaintext, aad []byte) ([]byte, error) {
	nonceSize := c.aead.NonceSize()
	out := make([]byte, 1+nonceSize, 1+nonceSize+len(plaintext)+c.aead.Overhead())
	out[0] = cipherVersion
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, fmt.Errorf("store: generate nonce: %w", err)
	}
	return c.aead.Seal(out, out[1:1+nonceSize], plaintext, aad), nil
}

// Decrypt opens a ciphertext produced by Encrypt with the same aad.
func (c *Cipher) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < 1+nonceSize+c.aead.Overhead() || ciphertext[0] != cipherVersion {
		return nil, ErrDecrypt
	}
	plain, err := c.aead.Open(nil, ciphertext[1:1+nonceSize], ciphertext[1+nonceSize:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

// EncryptString is a convenience wrapper that treats an empty string as "no
// secret" and stores it as a NULL column value.
func (c *Cipher) EncryptString(s string, aad []byte) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return c.Encrypt([]byte(s), aad)
}

// DecryptString reverses EncryptString.
func (c *Cipher) DecryptString(b []byte, aad []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	p, err := c.Decrypt(b, aad)
	if err != nil {
		return "", err
	}
	return string(p), nil
}
