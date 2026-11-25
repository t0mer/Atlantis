// Package secret encrypts stored credentials (the Deco password, channel
// URLs and tokens) with AES-256-GCM under ATLANTIS_SECRET_KEY.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// prefix marks a sealed value so plaintext is never mistaken for one.
const prefix = "v1:"

// Box seals and opens secrets. It is safe for concurrent use.
type Box struct{ aead cipher.AEAD }

// NewKey returns a random key, base64-encoded, for ATLANTIS_SECRET_KEY.
func NewKey() string {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// New parses a base64 32-byte key.
func New(key string) (*Box, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		return nil, errors.New("secret: ATLANTIS_SECRET_KEY is not valid base64")
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("secret: ATLANTIS_SECRET_KEY must be 32 bytes, got %d", len(k))
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext. The empty string stays empty.
func (b *Box) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.RawStdEncoding.EncodeToString(ct), nil
}

// ErrBadSecret means a stored value cannot be decrypted (wrong key or
// tampered data).
var ErrBadSecret = errors.New("secret: cannot decrypt (wrong ATLANTIS_SECRET_KEY?)")

// Open decrypts a value produced by Seal. The empty string stays empty.
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return "", ErrBadSecret
	}
	ct, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil || len(ct) < b.aead.NonceSize() {
		return "", ErrBadSecret
	}
	n := b.aead.NonceSize()
	pt, err := b.aead.Open(nil, ct[:n], ct[n:], nil)
	if err != nil {
		return "", ErrBadSecret
	}
	return string(pt), nil
}
